package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
)

// Options are the launch parameters `cenci open` collects: which agent and
// model to run, the instance name (empty = not given), attach/isolation
// modifiers, and the verbatim passthrough args for the agent CLI.
type Options struct {
	Agent       string
	Model       string
	Name        string
	Shell       bool
	Docker      bool
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
	case "claude", "codex":
		return nil
	}
	return usageErrorf("unknown agent %q. Valid agents: claude, codex.", agent)
}

// DefaultModel returns the per-agent model default applied when neither a
// shortcut nor an explicit --model chose one.
func DefaultModel(agent string) string {
	if agent == "codex" {
		return "gpt-5.6-terra"
	}
	return "sonnet"
}

// Launch runs the interactive path: scope the launch, make sure the image
// exists, wire cenci, then attach to a running container or create a
// detached one and attach into it. On success it does not return — the final
// attach execs the container runtime in place of this process.
func (e *Engine) Launch(opts Options) error {
	agent := opts.Agent
	if agent == "" {
		agent = "claude"
	}
	if err := ValidateAgent(agent); err != nil {
		return err
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel(agent)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	scope := ComputeScope(agent, opts.Name, cwd, home)

	if err := e.EnsureImage(scope); err != nil {
		return err
	}

	// Claude is a self-contained binary bind-mounted from the host; Codex is
	// baked into the image (npm launcher + nested native binary can't be
	// single-file mounted), so only Claude needs a host binary here.
	var agentBin string
	if agent == "claude" {
		agentBin, err = resolveHostBinary("claude")
		if err != nil {
			return fmt.Errorf("%s binary not found. Install %s on the host first.", agent, agent) //nolint:staticcheck // user-facing message ported verbatim from cenci-sand
		}
	}

	cenciBin, socketDir, cenciAvailable := e.resolveCenciWiring()

	// The container is the security boundary: the agent runs with full
	// permissions inside it (rejected if root — we run as uid 1000 'dev').
	var agentCmdArgs []string
	if agent == "codex" {
		agentCmdArgs = []string{"--dangerously-bypass-approvals-and-sandbox", "--model", model}
	} else {
		agentCmdArgs = []string{"--dangerously-skip-permissions", "--model", model}
	}

	execEnvArgs := []string{"-u", "dev",
		"-e", "TMUX_PANE=" + os.Getenv("TMUX_PANE"),
		"-e", "CENCI_SANDBOX=1",
		"-e", "CENCI_SANDBOX_AGENT=" + agent}
	if v := os.Getenv("CONTEXT7_API_KEY"); v != "" {
		execEnvArgs = append(execEnvArgs, "-e", "CONTEXT7_API_KEY="+v)
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		execEnvArgs = append(execEnvArgs, "-e", "OPENAI_API_KEY="+v)
	}

	// Attach to an already-running container: its mounts are fixed for its
	// whole lifetime, so only warn about missing cenci wiring (#195) and wait
	// for the async entrypoint when the lifecycle label says we created it
	// detached.
	running, err := e.containerRunning(scope.ContainerName)
	if err != nil {
		return err
	}
	if running {
		e.warnIfUnwired(scope.ContainerName, cenciAvailable)
		if e.lifecycleLabel(scope.ContainerName) == "detached" {
			if err := e.waitUntilReady(scope.ContainerName); err != nil {
				return err
			}
		}
		return e.runAgent(scope.ContainerName, agent, agentCmdArgs, execEnvArgs, opts)
	}

	// Remove a stopped container of the same name if one exists.
	_ = exec.Command(e.Runtime, "rm", scope.ContainerName).Run()

	runArgs, err := e.assembleRunArgs(agent, agentBin, cenciBin, socketDir, cenciAvailable, scope, opts, home)
	if err != nil {
		return err
	}

	// Start the shared container detached, then exec this agent into it: PID 1
	// stays independent from every tmux pane, so closing the first pane can't
	// SIGHUP the container and tear down later exec sessions.
	runArgs = append(runArgs, scope.Image, "-c", "touch /tmp/cenci-ready && exec sleep infinity")
	create := exec.Command(e.Runtime, append([]string{"run"}, runArgs...)...)
	create.Stdout = nil // cenci-sand discards the container id (>/dev/null)
	create.Stderr = e.Stderr
	if err := create.Run(); err != nil {
		return fmt.Errorf("%s run: %w", e.Runtime, err)
	}

	// The entrypoint performs credential and plugin setup before running the
	// readiness command. Do not race the first agent against that
	// initialization.
	if err := e.waitUntilReady(scope.ContainerName); err != nil {
		return err
	}
	return e.runAgent(scope.ContainerName, agent, agentCmdArgs, execEnvArgs, opts)
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
// (appended by the caller, Launch, after this returns) must stay last.
func (e *Engine) assembleRunArgs(agent, agentBin, cenciBin, socketDir string, cenciAvailable bool, scope Scope, opts Options, home string) ([]string, error) {
	args := e.baseRunArgs(scope)
	args = append(args, e.assembleVolumeMounts(agent, agentBin, cenciBin, socketDir, cenciAvailable, scope, home)...)
	args = append(args, e.assembleEnv(agent, scope, opts)...)

	credArgs, err := e.validateCredentials(agent, home)
	if err != nil {
		return nil, err
	}
	args = append(args, credArgs...)

	args = append(args, e.assembleOptionalFeatures(opts)...)

	return args, nil
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
// and home volumes, the claude binary (read-only; codex is baked into the
// image instead), git config (read-only, if present), the optional cenci
// binary + host socket dir wiring (paired with its own XDG_RUNTIME_DIR/
// TMUX_PANE env under the same cenciAvailable guard as the mount itself),
// claude credentials staging, and GitHub CLI credentials staging. Codex
// credentials are handled separately by validateCredentials, since a missing
// codex auth source is a hard launch error rather than an optional mount.
func (e *Engine) assembleVolumeMounts(agent, agentBin, cenciBin, socketDir string, cenciAvailable bool, scope Scope, home string) []string {
	args := []string{
		"-v", scope.WorkspaceBindHost + ":" + workspaceContainer,
		"-v", scope.VolumeName + ":/home/dev",
	}

	// Claude binary (read-only); Codex is baked into the image instead.
	if agent == "claude" {
		args = append(args, "-v", agentBin+":/usr/local/bin/claude:ro")
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

	// Cenci (optional).
	if cenciAvailable {
		args = append(args,
			"-v", cenciBin+":/usr/local/bin/cenci:ro",
			"-v", socketDir+":"+cenciSocketMountDest+":ro",
			"-e", "XDG_RUNTIME_DIR=/run/user/1000",
			"-e", "TMUX_PANE="+os.Getenv("TMUX_PANE"),
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

	return args
}

// assembleEnv builds the non-credential, non-optional-feature -e flags:
// TERM (with the xterm-256color fallback), the CENCI_SANDBOX marker/agent
// name, the HOST_UID/HOST_GID/WORKSPACE_SCOPE entrypoint contract, the
// opt-in reseed-creds flag, and the COLORTERM/CONTEXT7_API_KEY passthroughs.
func (e *Engine) assembleEnv(agent string, scope Scope, opts Options) []string {
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}

	args := []string{
		"-e", "TERM=" + term,
		"-e", "CENCI_SANDBOX=1",
		"-e", "CENCI_SANDBOX_AGENT=" + agent,
		"-e", fmt.Sprintf("HOST_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("HOST_GID=%d", os.Getgid()),
		"-e", "WORKSPACE_SCOPE=" + scope.WorkspaceScope,
	}

	// Force a reseed from the host (recovery after the volume's token chain
	// died, e.g. all sessions were revoked).
	if opts.ReseedCreds {
		args = append(args, "-e", "CENCI_SANDBOX_RESEED_CREDS=1")
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
// the corresponding mount/env args. Only codex has a hard requirement here
// (ChatGPT sign-in and/or API key — fail hard if neither is present); claude
// credentials are optional staging handled in assembleVolumeMounts.
func (e *Engine) validateCredentials(agent, home string) ([]string, error) {
	if agent != "codex" {
		return nil, nil
	}

	var args []string
	hasAuth := false
	codexAuth := filepath.Join(home, ".codex", "auth.json")
	if isRegularFile(codexAuth) {
		args = append(args, "-v", codexAuth+":/tmp/host-codex-creds/auth.json:ro")
		hasAuth = true
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		args = append(args, "-e", "OPENAI_API_KEY="+v)
		hasAuth = true
	}
	if !hasAuth {
		return nil, fmt.Errorf("--agent codex requires Codex auth. Run 'codex login' on the host (creates ~/.codex/auth.json) or export OPENAI_API_KEY.") //nolint:staticcheck // user-facing message ported verbatim from cenci-sand
	}

	return args, nil
}

// assembleOptionalFeatures builds the flags for opt-in isolation-weakening
// features: --network host (with a warning, since the container is the
// security boundary) and the docker/podman socket mount for DooD (docker
// first, falling back to a discoverable podman.sock, warning if neither is
// found).
func (e *Engine) assembleOptionalFeatures(opts Options) []string {
	var args []string

	// Host network mode (fallback for manual OAuth inside container).
	if opts.HostNetwork {
		_, _ = fmt.Fprintln(e.Stderr, "Warning: --host-network weakens the container's isolation boundary (the container is the security boundary); only use it for manual OAuth callback.")
		args = append(args, "--network", "host")
	}

	// Docker socket (opt-in DooD).
	if opts.Docker {
		dockerSock := ""
		if isSocket("/var/run/docker.sock") {
			dockerSock = "/var/run/docker.sock"
		} else {
			runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
			if runtimeDir == "" {
				runtimeDir = "/run/user/" + strconv.Itoa(os.Getuid())
			}
			if candidate := filepath.Join(runtimeDir, "podman", "podman.sock"); isSocket(candidate) {
				dockerSock = candidate
			}
		}
		if dockerSock != "" {
			_, _ = fmt.Fprintln(e.Stderr, "Warning: --docker bind-mounts the host's container runtime socket into the sandbox writable; a writable runtime socket is root-equivalent to the host, so only use it when you trust the sandbox's workload.")
			args = append(args, "-v", dockerSock+":/var/run/docker.sock")
		} else {
			_, _ = fmt.Fprintln(e.Stderr, "Warning: --docker requested but no container runtime socket found.")
		}
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
func (e *Engine) lifecycleLabel(name string) string {
	out, err := exec.Command(e.Runtime, "inspect", "--format",
		`{{ index .Config.Labels "cenci-sand.lifecycle" }}`, name).Output()
	if err != nil {
		return ""
	}
	return trimTrailingNewline(string(out))
}

// warnIfUnwired detects a running container that was created while the host
// events socket was missing — attaching can never bring the cenci wiring
// back (mounts are fixed for the container's lifetime), so say how to fix it
// instead of letting sessions silently vanish from the host status bars
// (#195).
func (e *Engine) warnIfUnwired(name string, cenciAvailable bool) {
	if !cenciAvailable {
		return
	}
	out, err := exec.Command(e.Runtime, "inspect", "--format",
		`{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}`, name).Output()
	if err != nil {
		return
	}
	for _, line := range splitLines(string(out)) {
		if line == cenciSocketMountDest {
			return
		}
	}
	_, _ = fmt.Fprintf(e.Stderr, "Warning: container '%s' was created without cenci wiring; its sessions will not report to the host status bars. Run '%s stop %s' and relaunch to fix.\n", name, e.Runtime, name)
}

// waitUntilReady polls for the entrypoint's /tmp/cenci-ready marker so the
// first agent never races credential/plugin initialization.
func (e *Engine) waitUntilReady(name string) error {
	for attempt := 0; attempt < readyPollAttempts; attempt++ {
		if exec.Command(e.Runtime, "exec", "-u", "dev", name, "test", "-e", "/tmp/cenci-ready").Run() == nil {
			return nil
		}
		time.Sleep(readyPollInterval)
	}
	return fmt.Errorf("container '%s' did not become ready within 60 seconds.", name) //nolint:staticcheck // user-facing message ported verbatim from cenci-sand
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

	argv := []string{runtimePath, "exec", "-it"}
	argv = append(argv, execEnvArgs...)
	argv = append(argv, name)
	if opts.Shell {
		_, _ = fmt.Fprintf(e.Stdout, "Attaching shell to running '%s'...\n", name)
		argv = append(argv, "/bin/bash")
	} else {
		argv = append(argv, agent)
		argv = append(argv, agentCmdArgs...)
		argv = append(argv, opts.AgentArgs...)
	}
	return execAttach(runtimePath, argv, os.Environ())
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
