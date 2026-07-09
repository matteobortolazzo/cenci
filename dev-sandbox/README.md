# Dev Sandbox (claude-sand)

> Part of [claude-tools](../README.md) — the **isolation layer**. See the root README for
> the one-command install and how the isolation, workflow, and attention layers fit together.

Isolated Docker/Podman container for running Claude Code with full permissions. Your host OS stays clean — only `~/Repos` is shared with the container.

## Prerequisites

- Docker or Podman installed on the host
- Claude Code installed on the host (`claude` in PATH)
- Host user UID must be 1000 (standard Linux default)

## Setup

Install the plugin from the marketplace, then run the setup skill — it symlinks the
`claude-sand` launcher onto your PATH and builds the container image:

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install sandbox
/sandbox:setup
```

`/sandbox:setup` accepts `--link-only` (symlink only, skip the build) or `--build-only`
(rebuild the image, skip the symlink). Update later with `claude plugin update sandbox`,
then re-run `/sandbox:setup --build-only` if the Dockerfile changed.

<details>
<summary>Manual setup (without the plugin)</summary>

```bash
# Symlink the launcher to your PATH
ln -s "$(pwd)/dev-sandbox/claude-sand" ~/.local/bin/claude-sand

# Build the image
claude-sand --build
```

</details>

## Usage

```bash
# Launch Claude Code (full permissions — the container is the security boundary)
claude-sand

# Pass additional args to Claude Code
claude-sand -p "fix the tests"
claude-sand --model sonnet

# Launch Codex instead of Claude Code
claude-sand --agent codex
codex-sand              # equivalent — the codex-sand symlink defaults to --agent codex
codex-sand -p "fix the tests"

# Open a bash shell for manual setup / troubleshooting
claude-sand --shell

# Run a named instance (separate home volume)
claude-sand --name myproject

# Rebuild the image (after changing Dockerfile or SDK versions)
claude-sand --build

# Enable Docker socket mounting (for TestContainers, docker build, etc.)
claude-sand --docker --shell
claude-sand --docker -p "run the integration tests"

# Use host networking for manual OAuth (browser callback)
claude-sand --host-network --shell
```

The container starts in the directory matching your host `$PWD` (mapped through `~/Repos` → `/workspace`).

If a container with the same name is already running, the script attaches to it instead of creating a new one.

### Choosing an agent

`claude-sand` launches Claude Code by default. Pass `--agent codex` (or use the `codex-sand`
symlink, which detects its invoked name) to launch Codex instead. Both agents run at full
permission inside the container — Claude with `--dangerously-skip-permissions`, Codex with
`--dangerously-bypass-approvals-and-sandbox`.

Containers and home volumes are **namespaced by agent**, so the two never collide:
`claude-sand` uses `claude-sand-<name>` / `claude-sand-home-<name>`; `codex-sand` (or
`--agent codex`) uses `codex-sand-<name>` / `codex-sand-home-<name>`. The Dockerfile is
unchanged — the agent binary is bind-mounted from the host (like Claude), so Codex needs no
image rebuild.

## First-Run Setup

If `~/.claude/.credentials.json` and `~/.config/gh/hosts.yml` exist on the host, they are automatically injected into the container on each start. **No manual auth needed.**

### Codex auth

When launching Codex (`--agent codex` / `codex-sand`), auth is staged from the host:

- `~/.codex/auth.json` — the ChatGPT sign-in credentials created by `codex login` on the
  host. Injected read-only and copied to `/home/dev/.codex/auth.json` (mode 600) on start.
- `OPENAI_API_KEY` — forwarded into the container when set in your host environment.

At least one of these must be present. If neither is, `claude-sand --agent codex` fails
hard with a clear message and does **not** create a container:

```
Error: --agent codex requires Codex auth. Run 'codex login' on the host
(creates ~/.codex/auth.json) or export OPENAI_API_KEY.
```

If host credentials are not available, open a shell for manual setup:

```bash
claude-sand --shell

# Inside the container:
gh auth login              # GitHub CLI auth
claude                     # Claude Code auth (first launch)
claude plugin install ...  # Install any plugins you need
```

For OAuth flows that require a browser callback, use host network mode:

```bash
claude-sand --host-network --shell
# Inside the container, run: claude
```

Everything persists in the home volume — only needs to happen once per instance.

## What's Included

| Tool | Version | Build arg override |
|------|---------|-------------------|
| .NET SDK | 10.0.100 | `DOTNET_SDK_VERSION` |
| Node.js | 24.x | `NODE_MAJOR` |
| Go | 1.24.1 | `GO_VERSION` |
| GitHub CLI | latest | — |
| git, ripgrep, jq, curl | latest | — |
| build-essential | latest | — |
| Python 3 | latest | — |
| uv | latest | `UV_VERSION` |
| Docker CLI | latest | — |

Override versions at build time:

```bash
docker build --build-arg DOTNET_SDK_VERSION=10.0.200 \
             --build-arg GO_VERSION=1.25.0 \
             -t claude-sandbox:latest dev-sandbox/
```

## Architecture

### Permission model

Claude Code runs with `--dangerously-skip-permissions` inside the container: no permission prompts, no tool allowlists. Isolation comes from the container itself, not from Claude Code's permission system. This is the supported use of the flag (it refuses to run as root; the container user is `dev`, UID 1000). Human-in-the-loop control moves up a layer — to workflow gates (plan approval, `AskUserQuestion`) rather than per-command approval.

Codex (`--agent codex`) runs with the direct analog, `--dangerously-bypass-approvals-and-sandbox`: it skips all confirmation prompts and runs commands without Codex's own sandbox, since the flag is intended for externally-sandboxed environments. It is container-safe by the same reasoning as Claude's flag — the container is the security boundary, and we run as `dev`, UID 1000. Unlike Claude's bypass mode there is no persisted "accept" dialog to seed, so the entrypoint does no Codex settings-seeding.

Bypass mode is **fully unattended**. The entrypoint seeds `/home/dev/.claude/settings.json` with `skipDangerousModePermissionPrompt: true` and `permissions.defaultMode: bypassPermissions` (and the image sets `IS_SANDBOX=1`), so even a brand-new `--name` instance on a fresh home volume reaches the prompt with no "Yes, I accept" bypass dialog, and headless `claude -p` runs report `bypassPermissions` instead of silently downgrading to `default`. The settings are deep-merged into any existing file, so unrelated keys survive.

**Security invariant — container-only.** The `skipDangerousModePermissionPrompt` / `defaultMode: bypassPermissions` pair lives *only* in the container home volume (`/home/dev/.claude/settings.json`). It must **never** be added to the host `~/.claude/settings.json`, and `claude-sand` never mounts the host `~/.claude` config dir (staging `.credentials.json` read-only is the single exception). The container boundary is the only thing that makes bypass mode safe — if a dialog ever shows where it shouldn't, the fix is always container-side, never host-side.

### Isolation

- Container has its **own home directory** (`/home/dev`) backed by a named Docker volume
- Only `~/Repos` from the host is mounted at `/workspace`
- Outbound network only (no inbound ports published)

### What persists (home volume)

| Path | Contents |
|------|----------|
| `/home/dev/.claude/` | Claude Code config, plugins, session data |
| `/home/dev/.codex/` | Codex config, auth, session data |
| `/home/dev/.npm/` | npm package cache |
| `/home/dev/.nuget/` | NuGet package cache |
| `/home/dev/.dotnet/` | .NET user-level config |
| `/home/dev/go/` | Go modules and build cache |
| `/home/dev/.config/gh/` | GitHub CLI auth token |
| `/home/dev/.bash_history` | Shell history |

### What's bind-mounted read-only

| Host path | Container path | Purpose |
|-----------|---------------|---------|
| Agent binary (`claude` or `codex`) | `/usr/local/bin/<agent>` | Always matches host version |
| `~/.config/git/config` or `~/.gitconfig` | `/home/dev/.gitconfig` | Git identity |
| `~/.claude/.credentials.json` | `/tmp/host-claude-creds/` (staging) | Claude OAuth tokens (copied to home on start) |
| `~/.codex/auth.json` (Codex only) | `/tmp/host-codex-creds/` (staging) | Codex OAuth tokens (copied to home on start) |
| `~/.config/gh/hosts.yml` | `/tmp/host-gh-config/` (staging) | GitHub CLI tokens (copied to home on start) |

### MCP servers

MCP servers are picked up from project-scoped `.mcp.json` files inside the workspace (e.g. `./.mcp.json` under the project you're working on). The launcher forwards `CONTEXT7_API_KEY` from the host when set, so `.mcp.json` entries referencing `${CONTEXT7_API_KEY}` resolve correctly inside the container.

### Docker (optional, opt-in)

Mount the host Docker/Podman socket into the container for Docker-outside-of-Docker (DooD):

```bash
claude-sand --docker
```

This enables:
- **TestContainers**: Integration tests that spin up containers (databases, message brokers, etc.)
- **Docker CLI**: Build images, run containers, use docker compose
- **Any Docker SDK usage**: Libraries that talk to the Docker daemon

The entrypoint automatically detects the socket's group ownership and adds the `dev` user to the matching group.

**Security note**: The `--docker` flag grants the container access to the host's Docker daemon. Any container started from within the sandbox runs on the host, with full Docker privileges. This is why it is opt-in.

### Agentwatch (optional)

If `agentwatch` is installed on the host and the daemon is running, the script automatically:
- Bind-mounts the `agentwatch` binary (read-only)
- Bind-mounts the events socket so hooks can reach the host daemon
- Passes `$TMUX_PANE` for tmux window status updates

Install the agentwatch plugin inside the container: `claude plugin install agentwatch`

> **Renamed from muxwatch.** The event socket moved from `muxwatch-events.sock` to
> `agentwatch-events.sock`. If you previously ran the `muxwatch` daemon, rebuild/reinstall
> the `agentwatch` binary and restart the daemon so it creates the new socket the launcher
> now bind-mounts — otherwise agentwatch is silently skipped inside the container.

### Container lifecycle

- Containers are created with `--rm` — removed automatically on exit
- The home volume survives container removal
- Each `--name` instance gets its own container and volume

## Maintenance

### Update SDK versions

Edit the `ARG` lines at the top of the `Dockerfile`, then rebuild:

```bash
claude-sand --build
```

### Update Claude Code

Just update Claude Code on the host. The binary is bind-mounted, so the container always uses the host version.

### Reset an instance

Delete the home volume to start fresh (caches, auth, config all cleared):

```bash
docker volume rm claude-sand-home-default
# or for a named instance:
docker volume rm claude-sand-home-myproject
```

### List instances

```bash
docker volume ls --filter name=claude-sand-home
```

### Clean up everything

```bash
# Remove all sandbox volumes
docker volume ls --filter name=claude-sand-home -q | xargs docker volume rm

# Remove the image
docker rmi claude-sandbox:latest
```

## Sharing the Image

### Via container registry

```bash
docker tag claude-sandbox:latest ghcr.io/YOUR_ORG/claude-sandbox:latest
docker push ghcr.io/YOUR_ORG/claude-sandbox:latest
```

Recipients pull the image and only need the `claude-sand` script.

### Via file export

```bash
# Export
docker save claude-sandbox:latest | gzip > claude-sandbox.tar.gz

# Import on another machine
docker load < claude-sandbox.tar.gz
```

## Troubleshooting

**Permission errors on `/workspace` files**
Your host UID must be 1000. Check with `id -u`.

**`claude` binary not found**
Ensure `claude` is in your host PATH. Check with: `readlink -f "$(which claude)"`

**Container runtime**
The script auto-detects `podman` first, then falls back to `docker`.

**Claude Code says "request not found" during OAuth**
The OAuth callback can't reach the container. Either:
1. Ensure `~/.claude/.credentials.json` exists on the host (run `claude` on the host first to authenticate), or
2. Use `claude-sand --host-network --shell` and run `claude` to complete the OAuth flow with host networking.
