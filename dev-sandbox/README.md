# agent-sandbox (agent-sand)

> Part of [agent-stack](../README.md) — the **isolation layer**. See the root README for
> the one-command install and how the isolation, workflow, and attention layers fit together.

Isolated Docker/Podman container for running Claude Code with full permissions. Your host OS stays clean — each launch mounts only the current repo (not your whole `~/Repos`) into its own container.

## Prerequisites

- Docker or Podman installed on the host
- Claude Code installed on the host (`claude` in PATH)
- Codex auth on the host when using `--agent codex` — run `codex login` to create
  `~/.codex/auth.json`, or export `OPENAI_API_KEY`. Codex itself is baked into the
  image, so it does **not** need to be installed on the host.
- Host user UID must be 1000 (standard Linux default)

## Setup

The easiest path is the [one-command installer](../docs/getting-started.md), which
installs the plugin, symlinks the launchers, and offers to build the image:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash
```

Or install the plugin from the marketplace, then run the setup skill — it symlinks the
`agent-sand` launcher onto your PATH and builds the container image:

```bash
claude plugin marketplace add matteobortolazzo/agent-stack
claude plugin install agent-sandbox
/agent-sandbox:setup
```

`/agent-sandbox:setup` accepts `--link-only` (symlink only, skip the build) or `--build-only`
(rebuild the image, skip the symlink). Update later with `claude plugin update agent-sandbox`,
then re-run `/agent-sandbox:setup --build-only` if the Dockerfile changed.

The `setup` skill is Claude Code-only because it relies on Claude's interactive and
plugin-root extensions. Codex users should use the agent-stack installer, which
installs the same agent-sandbox plugin for Codex and performs the launcher setup outside the
agent session.

<details>
<summary>Manual setup (without the plugin)</summary>

```bash
# Symlink the launcher to your PATH
ln -s "$(pwd)/dev-sandbox/agent-sand" ~/.local/bin/agent-sand

# Build the image
agent-sand --build
```

</details>

## Usage

```bash
# Launch Claude Code (full permissions — the container is the security boundary)
agent-sand

# Pass additional args to Claude Code
agent-sand -p "fix the tests"
agent-sand --model sonnet

# Launch Codex instead of Claude Code
agent-sand --agent codex
codex-sand              # equivalent — the codex-sand symlink defaults to --agent codex
codex-sand -p "fix the tests"

# Open a bash shell for manual setup / troubleshooting
agent-sand --shell

# Run a named instance (extra suffix, for parallel isolation e.g. worktrees)
agent-sand --name myproject

# Rebuild the image (after changing Dockerfile or SDK versions)
agent-sand --build

# Rebuild only the base image (after changing Dockerfile.base)
agent-sand --build-base

# Enable Docker socket mounting (for TestContainers, docker build, etc.)
agent-sand --docker --shell
agent-sand --docker -p "run the integration tests"

# Use host networking for manual OAuth (browser callback)
agent-sand --host-network --shell

# Force an agentflow/agentwatch plugin update now (bypasses the 30-min TTL)
agent-sand --update-plugins
```

### Per-repo containers

Run `agent-sand` from inside a git repo (or any subdirectory of one) and it mounts
**only that repo's root** at `/workspace` — not your whole `~/Repos`. The container
`WORKDIR` mirrors your host `$PWD` relative to the repo root, so launching from a
subdirectory starts the shell/agent in the matching `/workspace/<subpath>`.

The container name and home volume are derived from the repo directory name (slugified):
`<agent>-sand-<repo-slug>` / `<agent>-sand-home-<repo-slug>`. Pass `--name` to append an
extra suffix (`<agent>-sand-<repo-slug>-<name>`) when you need more than one sandbox for
the same repo in parallel — e.g. one per git worktree.

If a repo's directory contains `.agent-sand/Dockerfile`, that repo gets its own thin
image (`agent-sandbox-<repo-slug>:latest`, built `FROM` the shared base image) instead of
the monolith — see [Per-repo images](#per-repo-images) below. Repos without that file
still get single-repo mounting, just using the shared `agent-sandbox:latest` image.

**Clean break:** per-repo launches always start from a fresh `<agent>-sand-home-<repo-slug>`
volume — there is no automatic migration from an existing `<agent>-sand-home-default`
volume created by an older whole-`~/Repos` launch. Re-authenticate (or delete the old
volume once you've confirmed you don't need it — see
[Reset an instance](#reset-an-instance)).

**Caveat:** two different repos that share the same directory basename (e.g.
`~/Repos/foo/api` and `~/work/foo/api`, both named `api`) slugify to the same name and
would collide on the same container/volume. Use `--name` to disambiguate if you work
with same-named repos side by side.

Running `agent-sand` **outside** any git repo falls back to the legacy scheme: the whole
`~/Repos` directory is mounted at `/workspace`, and the container/volume are named
`<agent>-sand-<name>` / `<agent>-sand-home-<name>` (default name `default`) — unchanged
from previous versions, so existing `<agent>-sand-home-default`-style volumes keep
working untouched.

If a container with the same name is already running, the script attaches to it instead of creating a new one.

### Choosing an agent

`agent-sand` launches Claude Code by default. Pass `--agent codex` (or use the `codex-sand`
symlink, which detects its invoked name) to launch Codex instead. Both agents run at full
permission inside the container — Claude with `--dangerously-skip-permissions`, Codex with
`--dangerously-bypass-approvals-and-sandbox`.

Containers and home volumes are **namespaced by agent**, so the two never collide:
the **Claude agent** uses the `claude-sand-` prefix; `codex-sand` (or `agent-sand --agent
codex`) uses `codex-sand-`. The rest of the name is the repo slug (or the legacy `<name>`
outside a git repo — see [Per-repo containers](#per-repo-containers) above), e.g.
`claude-sand-my-project` / `claude-sand-home-my-project`. The two agents
are provisioned differently: **Claude** is bind-mounted from the host (self-contained binary,
so the container always matches your host version). **Codex** is baked into the image — it ships
as an npm launcher that resolves a native binary nested in its own `node_modules`, which a
single-file bind-mount can't carry. Updating Codex therefore means bumping `CODEX_VERSION` in
the Dockerfile and rebuilding (`agent-sand --build`), whereas updating Claude needs no rebuild.

## First-Run Setup

If `~/.claude/.credentials.json` and `~/.config/gh/hosts.yml` exist on the host, they are automatically injected into the container on each start. **No manual auth needed.**

### Onboarding prompts

Claude Code's first-run wizard — the theme picker, the terminal "anti-flicker"
setup, and the account/login step — is driven by `/home/dev/.claude.json`, which
lives in the persistent home volume. The entrypoint seeds
`hasCompletedOnboarding` on start, so a fresh instance jumps straight to a usable
session and you won't see those prompts. If an older volume still shows them
once, completing the wizard is recorded in the volume and won't recur for that
instance. They only reappear for a **new `--name` instance** (its own fresh
volume) or after you reset/delete a volume.

### Codex auth

When launching Codex (`--agent codex` / `codex-sand`), auth is staged from the host:

- `~/.codex/auth.json` — the ChatGPT sign-in credentials created by `codex login` on the
  host. Injected read-only and copied to `/home/dev/.codex/auth.json` (mode 600) on start.
- `OPENAI_API_KEY` — forwarded into the container when set in your host environment.

At least one of these must be present. If neither is, `agent-sand --agent codex` fails
hard with a clear message and does **not** create a container:

```
Error: --agent codex requires Codex auth. Run 'codex login' on the host
(creates ~/.codex/auth.json) or export OPENAI_API_KEY.
```

If host credentials are not available, open a shell for manual setup:

```bash
agent-sand --shell

# Inside the container:
gh auth login              # GitHub CLI auth
claude                     # Claude Code auth (first launch)
claude plugin install ...  # Install any plugins you need
```

For OAuth flows that require a browser callback, use host network mode:

```bash
agent-sand --host-network --shell
# Inside the container, run: claude
```

Everything persists in the home volume — only needs to happen once per instance.

## What's Included

| Tool | Version | Build arg override |
|------|---------|-------------------|
| .NET SDK | 10.0.100 | `DOTNET_SDK_VERSION` |
| Node.js | 24.x | `NODE_MAJOR` |
| Go | 1.24.1 | `GO_VERSION` |
| Codex CLI | 0.144.1 | `CODEX_VERSION` |
| GitHub CLI | latest | — |
| git, ripgrep, jq, curl | latest | — |
| build-essential | latest | — |
| Python 3 | latest | — |
| uv | latest | `UV_VERSION` |
| Docker CLI | latest | — |

Override versions at build time. The monolith `Dockerfile` builds `FROM
agent-sandbox-base:${BASE_VERSION}`, so build (or pull) the base image first and pass
the matching `BASE_VERSION` (from `.claude-plugin/plugin.json`) — `agent-sand --build`
does both steps for you automatically:

```bash
docker build -f dev-sandbox/Dockerfile.base -t agent-sandbox-base:0.8.0 dev-sandbox/

docker build --build-arg BASE_VERSION=0.8.0 \
             --build-arg DOTNET_SDK_VERSION=10.0.200 \
             --build-arg GO_VERSION=1.25.0 \
             -t agent-sandbox:latest dev-sandbox/
```

## Architecture

### Two-image model: base + monolith

The image is built in two layers:

- **`Dockerfile.base`** → `agent-sandbox-base:<version>` (version comes from
  `.claude-plugin/plugin.json`). Stack-agnostic: Ubuntu 24.04, system packages, locale,
  `uv`, GitHub CLI, Docker CLI, the non-root `dev` user, and the entrypoint. No language
  runtimes. Rebuilt only when the base itself changes; `agent-sand --build-base` builds
  it explicitly, and `agent-sand --build` / `agent-sand` builds it automatically the
  first time (or whenever `agent-sandbox-base:<version>` is missing locally).
- **`Dockerfile`** → `agent-sandbox:latest`, `FROM agent-sandbox-base:${BASE_VERSION}`.
  Layers the runtime stacks on top: .NET SDK, Node.js, Codex CLI, Go. This is the image
  `agent-sand` actually runs.

`dev-sandbox/fragments/*.dockerfile` holds the same per-stack blocks (`dotnet`, `node`,
`go`, `python`, `rust`) as standalone snippets for future per-project image composition.
They are **not** built or composed automatically yet — that lands in a follow-up PR. Until
then, each fragment and its corresponding block in `Dockerfile` are kept byte-identical by
hand; when you change one, change the other the same way.

### Per-repo images

A repo can opt into its own thin image instead of the shared monolith by adding
`.agent-sand/Dockerfile` (and any files it needs, e.g. a fragment copy) under
`.agent-sand/` at the repo root. When present, `agent-sand` builds
`agent-sandbox-<repo-slug>:latest` `FROM agent-sandbox-base:${BASE_VERSION}` — the
same base image and `BASE_VERSION` as the monolith — using
`.agent-sand/` as the build context, and runs that instead of `agent-sandbox:latest`.
Repos without `.agent-sand/Dockerfile` keep using the shared monolith image, just with
single-repo mounting (see [Per-repo containers](#per-repo-containers)). Rebuild a
repo's own image the same way as the monolith: `agent-sand --build` (run from inside
that repo).

`/agentflow:configure` generates and maintains `.agent-sand/Dockerfile` automatically
from the repo's detected stack (question 9) — you normally don't hand-write this file.
It writes only the fragments the detected stack needs, wrapped in
`# agentflow:managed-begin` / `# agentflow:managed-end` markers so re-running configure
regenerates just that block and preserves anything the team appends around it.

**Sync obligation**: `dev-sandbox/fragments/*.dockerfile` is the source of truth for the
per-stack blocks configure assembles into `.agent-sand/Dockerfile`; the agentflow
`configure` skill's stack-to-fragment mapping table mirrors this directory. If a fragment
is added, removed, or renamed here, that table needs a matching manual update — low risk
in practice, since both live in this same monorepo and are maintained together, but
currently unenforced by tooling.

**Trust / security note**: a committed `.agent-sand/Dockerfile` is reviewed code, like
any other file in the PR that adds or changes it. It only runs `docker build` steps
assembled from `dev-sandbox/fragments/*.dockerfile` by configure's templates — no
arbitrary runtime hooks execute during generation or during the build it produces.

### Permission model

Claude Code runs with `--dangerously-skip-permissions` inside the container: no permission prompts, no tool allowlists. Isolation comes from the container itself, not from Claude Code's permission system. This is the supported use of the flag (it refuses to run as root; the container user is `dev`, UID 1000). Human-in-the-loop control moves up a layer — to workflow gates (plan approval, `AskUserQuestion`) rather than per-command approval.

Codex (`--agent codex`) runs with the direct analog, `--dangerously-bypass-approvals-and-sandbox`: it skips all confirmation prompts and runs commands without Codex's own sandbox, since the flag is intended for externally-sandboxed environments. It is container-safe by the same reasoning as Claude's flag — the container is the security boundary, and we run as `dev`, UID 1000. Unlike Claude's bypass mode there is no persisted "accept" dialog to seed, so the entrypoint does no Codex settings-seeding.

Bypass mode is **fully unattended**. The entrypoint seeds `/home/dev/.claude/settings.json` with `skipDangerousModePermissionPrompt: true` and `permissions.defaultMode: bypassPermissions` (and the image sets `IS_SANDBOX=1`), so even a brand-new `--name` instance on a fresh home volume reaches the prompt with no "Yes, I accept" bypass dialog, and headless `claude -p` runs report `bypassPermissions` instead of silently downgrading to `default`. The settings are deep-merged into any existing file, so unrelated keys survive.

**Security invariant — container-only.** The `skipDangerousModePermissionPrompt` / `defaultMode: bypassPermissions` pair lives *only* in the container home volume (`/home/dev/.claude/settings.json`). It must **never** be added to the host `~/.claude/settings.json`, and `agent-sand` never mounts the host `~/.claude` config dir (staging `.credentials.json` read-only is the single exception). The container boundary is the only thing that makes bypass mode safe — if a dialog ever shows where it shouldn't, the fix is always container-side, never host-side.

### Isolation

- Container has its **own home directory** (`/home/dev`) backed by a named Docker volume
- Only the current repo's root (not the whole `~/Repos`) is mounted at `/workspace` — see
  [Per-repo containers](#per-repo-containers)
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
| `claude` binary (Claude only) | `/usr/local/bin/claude` | Always matches host version (Codex is baked into the image instead) |
| `~/.config/git/config` or `~/.gitconfig` | `/home/dev/.gitconfig` | Git identity |
| `~/.claude/.credentials.json` | `/tmp/host-claude-creds/` (staging) | Claude OAuth tokens (copied to home on start) |
| `~/.codex/auth.json` (Codex only) | `/tmp/host-codex-creds/` (staging) | Codex OAuth tokens (copied to home on start) |
| `~/.config/gh/hosts.yml` | `/tmp/host-gh-config/` (staging) | GitHub CLI tokens (copied to home on start) |

### MCP servers

MCP servers are picked up from project-scoped `.mcp.json` files inside the workspace (e.g. `./.mcp.json` under the project you're working on). The launcher forwards `CONTEXT7_API_KEY` from the host when set, so `.mcp.json` entries referencing `${CONTEXT7_API_KEY}` resolve correctly inside the container.

### Docker (optional, opt-in)

Mount the host Docker/Podman socket into the container for Docker-outside-of-Docker (DooD):

```bash
agent-sand --docker
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

No manual install is needed inside the container. On each start the entrypoint
enables and installs the `agentwatch` and `agentflow` plugins from the
`agent-stack` marketplace, so sandbox sessions show up in the host status bar out
of the box, and keeps them current: it refreshes the marketplace clone and
updates any stale plugin, gated by a 30-minute stamp so rapid stop/start cycles
make zero network calls (`agent-sand --update-plugins` forces it — see
[Maintenance](#update-sandbox-plugins)). Existing home volumes are migrated off
the old `muxwatch`/`ccflow` plugins and the renamed `claude-tools` marketplace at
the same time.

### Container lifecycle

- Containers are created with `--rm` — removed automatically on exit
- The home volume survives container removal
- Each `--name` instance gets its own container and volume

## Maintenance

### Update SDK versions

Edit the `ARG` line for the stack you want to bump:

- `DOTNET_SDK_VERSION`, `NODE_MAJOR`, `CODEX_VERSION`, `GO_VERSION` live in `Dockerfile`
  (the monolith layers on top of the base).
- `UV_VERSION` lives in `Dockerfile.base` — bump it and run `agent-sand --build-base`
  first, then rebuild the monolith.

Then rebuild:

```bash
agent-sand --build
```

### Update Claude Code

Just update Claude Code on the host. The binary is bind-mounted, so the container always uses the host version — no rebuild needed.

### Update sandbox plugins

Nothing to do normally: on each container start the entrypoint refreshes the
`agent-stack` marketplace and updates stale `agentflow`/`agentwatch` plugins in
the home volume (TTL-gated to 30 minutes). To force an update immediately —
e.g. right after merging a plugin change — run:

```bash
agent-sand --update-plugins
```

It updates the running container in place (agent sessions pick the new version
up on their next start), or spins up a one-shot container against the home
volume if none is running.

### Update Codex

Codex is baked into the image, so updating it means bumping `CODEX_VERSION` in the
`Dockerfile` and rebuilding (`agent-sand --build`). Unlike Claude, updating Codex on the host
has no effect on the container.

A scheduled workflow (`.github/workflows/codex-version-bump.yml`) checks for new
Codex releases daily, opens a PR bumping `CODEX_VERSION`, and auto-merges it —
no manual bumping needed. Run `agent-sand --build` to pick up the new version
on your next rebuild (monolith image only).

### Reset an instance

Delete the home volume to start fresh (caches, auth, config all cleared). Claude Code
instances use `claude-sand-home-<repo-slug>`; Codex instances use
`codex-sand-home-<repo-slug>` (or `-<name>` outside a git repo — see
[Per-repo containers](#per-repo-containers)):

```bash
docker volume rm claude-sand-home-agent-stack
# or for a --name instance:
docker volume rm claude-sand-home-agent-stack-myproject
# outside a git repo (legacy scheme):
docker volume rm claude-sand-home-default
# Codex instances:
docker volume rm codex-sand-home-agent-stack
```

### List instances

```bash
docker volume ls --filter name=sand-home
```

### Clean up everything

```bash
# Remove all sandbox volumes (both Claude Code and Codex instances)
docker volume ls --filter name=sand-home -q | xargs docker volume rm

# Remove the image
docker rmi agent-sandbox:latest
```

## Sharing the Image

### Via container registry

```bash
docker tag agent-sandbox:latest ghcr.io/YOUR_ORG/agent-sandbox:latest
docker push ghcr.io/YOUR_ORG/agent-sandbox:latest
```

Recipients pull the image and only need the `agent-sand` script.

### Via file export

```bash
# Export
docker save agent-sandbox:latest | gzip > agent-sandbox.tar.gz

# Import on another machine
docker load < agent-sandbox.tar.gz
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
2. Use `agent-sand --host-network --shell` and run `claude` to complete the OAuth flow with host networking.
