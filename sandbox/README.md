# cenci-sandbox

> Part of [cenci](../README.md) — the **isolation layer**. See the root README for
> the one-command install and how the isolation, workflow, and attention layers fit together.

Container images and runtime for cenci: run Claude Code or Codex at full permissions
without giving the agent your whole host. Each launch mounts only the current
repository into an isolated Docker or Podman container.

The `cenci` binary is the entry point — `cenci open` (alias `cn`) launches or attaches
sessions, and `cenci sandbox <verb>` handles builds and maintenance. This project ships
the image and runtime assets it runs (Dockerfiles, fragments, `entrypoint.sh`,
container-side scripts). The full CLI reference — every verb, flag, and the one-token
shortcut table — lives in
[cenci-watch's README](../watch/README.md#sandbox-management-and-session-launching-cenci-sandbox-cenci-open).

![cenci-sandbox mounts the current repository into a deliberately small container boundary for a full-permission coding agent](../docs/assets/cenci-sandbox-boundary.svg)

## Prerequisites

- Docker or Podman installed on the host
- Claude Code installed on the host (`claude` in PATH) — only required when launching the
  **Claude agent** (the default, or `--agent claude`). Claude's binary is bind-mounted from
  the host into the container; `--agent codex` never touches it (Codex is baked into the
  image — see [Choosing an agent](#choosing-an-agent)).
- Codex auth on the host when using `--agent codex` — run `codex login` to create
  `~/.codex/auth.json`, or export `OPENAI_API_KEY`. Codex itself is baked into the
  image, so it does **not** need to be installed on the host.

## Limitations

**Legacy `~/Repos` mount and pre-existing files.** The container's `dev` user is baked
in at UID/GID 1000, but `entrypoint.sh` auto-remaps it to your host `HOST_UID`/`HOST_GID`
on every launch (the launcher passes them in), so files newly written into the per-repo
`/workspace` mount always come out owned by your host user — no manual `chown` needed.
Renumbering a live account requires no process running under it yet, so the container now
briefly starts as `root` for this remap step before `entrypoint.sh` unconditionally drops
privileges to (the host-remapped) `dev` for everything else — the exec/attach path
(`cenci open --shell`, agent sessions) is unaffected and always resolves to `dev`.
The one remaining caveat is the legacy whole-`~/Repos` mount (used outside a git repo):
the remap does **not** retroactively `chown` that tree, since rewriting ownership across
your entire `~/Repos` from inside the container is too large a blast radius to automate.
If you have pre-existing files there from before this fix, or you're not UID 1000 and hit
permission errors under the legacy mount, `chown -R $(id -u):$(id -g) ~/Repos` on the host
clears it up.

## Installation

The easiest path is the [one-command installer](../docs/getting-started.md), which
installs the plugin, puts the `cenci` binary (and its `cn` launch alias) on your PATH,
and offers to build the image:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
```

### Advanced / development: standalone setup

Install the plugin from the marketplace, then run the setup skill—it verifies the
`cenci` binary resolves on your PATH and builds the container image:

```bash
claude plugin marketplace add matteobortolazzo/cenci
claude plugin install cenci-sandbox
/cenci-sandbox:setup
```

`/cenci-sandbox:setup` accepts `--check-only` (verify only, skip the build) or `--build-only`
(rebuild the image, skip the verification). Update later with `claude plugin update cenci-sandbox`,
then re-run `/cenci-sandbox:setup --build-only` if the Dockerfile changed.

The `setup` skill is Claude Code-only because it relies on Claude's interactive and
plugin-root extensions. Codex users should use the cenci installer, which
installs the same cenci-sandbox plugin for Codex and sets up the `cenci` binary outside the
agent session.

<details>
<summary>Manual setup (without the plugin)</summary>

Install the `cenci` binary by hand (see
[Install the binary manually](../watch/README.md#install-the-binary-manually) in
cenci-watch's README) and make sure it resolves on your PATH — optionally with a
`cn` symlink next to it for the short launch alias. Then build the image:

```bash
cenci sandbox build
```

Without the installed plugin, point the launcher at a local checkout's assets:
`CENCI_SANDBOX_ASSETS=/path/to/cenci/sandbox cenci sandbox build`.

</details>

## Usage

```bash
# Launch or attach a session (full permissions — the container is the security boundary)
cn                    # Claude Code (`cn <args>` is exactly `cenci open <args>`)
cn xt                 # Codex, gpt-5.6-terra
cn ch                 # Claude, haiku (shortcuts: ch/cs/co/cf, xl/xt/xs)
cenci open --agent codex --model gpt-5.6-sol --name mybox

# Pass args through to the agent CLI — everything after a bare -- is forwarded verbatim
cn -- -p "fix the tests"
cn cs -- --resume

# Open a bash shell for manual setup / troubleshooting
cenci open --shell

# Build / maintain the images
cenci sandbox build   # (re)build the image
cenci sandbox prune   # clean up superseded base tags, dangling images, stopped sandbox containers
```

This is deliberately just a taste: the full launcher reference — every `cenci open`
flag (`--agent`, `--model`, `--name`, `--shell`, `--docker`, `--host-network`,
`--reseed-creds`), the `cenci sandbox` verbs (`build`, `build-base`, `prune`,
`update-plugins`, `reseed-creds`, `reap-orphans`, `ls`, `stop`), the shortcut table,
and the flag-parsing rules (unknown long flags are usage errors and exit 2; agent
flags go after `--`) — lives in
[cenci-watch's README](../watch/README.md#sandbox-management-and-session-launching-cenci-sandbox-cenci-open).

### Per-repo containers

Run the launcher (`cn` / `cenci open`) from inside a git repo (or any subdirectory of
one) and it mounts **only that repo's root** at `/workspace` — not your whole `~/Repos`.
The container `WORKDIR` mirrors your host `$PWD` relative to the repo root, so launching
from a subdirectory starts the shell/agent in the matching `/workspace/<subpath>`.

The container name and home volume are derived from the repo directory name (slugified):
`<agent>-cenci-<repo-slug>` / `<agent>-cenci-home-<repo-slug>`. Pass `--name` to append an
extra suffix (`<agent>-cenci-<repo-slug>-<name>`) when you need more than one sandbox for
the same repo in parallel — e.g. one per git worktree.

If a repo's directory contains `.cenci/Dockerfile`, that repo gets its own thin
image (`cenci-sandbox-<repo-slug>:latest`, built `FROM` the shared base image) instead of
the monolith — see [Per-repo images](#per-repo-images) below. Repos without that file
still get single-repo mounting, just using the shared `cenci-sandbox:latest` image.

**Clean break:** per-repo launches always start from a fresh `<agent>-cenci-home-<repo-slug>`
volume — there is no automatic migration from an existing `<agent>-cenci-home-default`
volume created by an older whole-`~/Repos` launch. Re-authenticate (or delete the old
volume once you've confirmed you don't need it — see
[Reset an instance](#reset-an-instance)).

**Caveat:** two different repos that share the same directory basename (e.g.
`~/Repos/foo/api` and `~/work/foo/api`, both named `api`) slugify to the same name and
would collide on the same container/volume. Use `--name` to disambiguate if you work
with same-named repos side by side.

Running `cenci open` **outside** any git repo falls back to the legacy scheme: the whole
`~/Repos` directory is mounted at `/workspace`, and the container/volume are named
`<agent>-cenci-<name>` / `<agent>-cenci-home-<name>` (default name `default`) — unchanged
from previous versions, so existing `<agent>-cenci-home-default`-style volumes keep
working untouched.

The repository container runs detached, and every shell or agent is launched as an
independent `exec` session inside it. Closing any terminal or tmux window therefore
ends only that shell or agent; it cannot terminate the other windows sharing the
container. If a container with the same name is already running, the launcher reuses
it.

### Choosing an agent

`cenci open` launches Claude Code by default. Pass `--agent codex` (or use an
`xl`/`xt`/`xs` shortcut) to launch Codex instead. Both agents run at full permission
inside the container — Claude with `--dangerously-skip-permissions`, Codex with
`--dangerously-bypass-approvals-and-sandbox`.

Containers and home volumes are **namespaced by agent**, so the two never collide:
the **Claude agent** uses the `claude-cenci-` prefix; the **Codex agent** (`--agent
codex` / `cn xt`) uses `codex-cenci-`. The rest of the name is the repo slug (or the legacy `<name>`
outside a git repo — see [Per-repo containers](#per-repo-containers) above), e.g.
`claude-cenci-my-project` / `claude-cenci-home-my-project`. The two agents
are provisioned differently: **Claude** is bind-mounted from the host (self-contained binary,
so the container always matches your host version). **Codex** is baked into the image — it ships
as an npm launcher that resolves a native binary nested in its own `node_modules`, which a
single-file bind-mount can't carry. Updating Codex therefore means bumping `CODEX_VERSION` in
the monolith and Codex fragment, regenerating any tailored repo Dockerfile, and rebuilding
(`cenci sandbox build`), whereas updating Claude needs no rebuild.

## First-Run Setup

If `~/.claude/.credentials.json` and `~/.config/gh/hosts.yml` exist on the host, they are automatically injected into the container. **No manual auth needed.**

Claude (and Codex) OAuth uses rotating refresh tokens: after the sandbox's first
token refresh, the volume's credentials become an independent login from the
host's — like being signed in on two machines. Claude and Codex credentials are
therefore **seeded only when the volume has none yet** and never overwritten on
later starts: each instance stays logged in indefinitely, and using the sandbox
all day can no longer log your host session out (you may see one final host
re-login right after a volume is first seeded, then both sides are stable). The
GitHub CLI token doesn't rotate, so `hosts.yml` is still refreshed from the host
on every start.

If an instance's login does die (e.g. you revoked all sessions on claude.ai),
force a one-time re-copy from the host:

```bash
cenci open --reseed-creds
# or the maintenance-verb alias:
cenci sandbox reseed-creds
```

### Onboarding prompts

Claude Code's first-run wizard — the theme picker, the terminal "anti-flicker"
setup, and the account/login step — is driven by `/home/dev/.claude.json`, which
lives in the persistent home volume. The entrypoint seeds
`hasCompletedOnboarding` on start, so a fresh instance jumps straight to a usable
session and you won't see those prompts. If an older volume still shows them
once, completing the wizard is recorded in the volume and won't recur for that
instance. They only reappear for a **new `--name` instance** (its own fresh
volume) or after you reset/delete a volume.

### Status lines

Both agents get a status line out of the box:

- **Claude Code** — [CCometixLine](https://github.com/Haleclipse/CCometixLine) (`ccline`)
  is baked into the base image at `/usr/local/bin/ccline`, and the entrypoint seeds a
  `statusLine` entry into the volume's `settings.json` **only if none is set** — customize
  it inside the container and your version wins.
- **Codex** — uses its native status line: the entrypoint seeds `[tui] status_line`
  (model, cwd, context usage, tokens, 5-hour/weekly rate limits) into
  `/home/dev/.codex/config.toml`. An existing `[tui]` table or `status_line` key is left
  untouched; tweak it with Codex's `/statusline` command.

### Codex auth

When launching Codex (`--agent codex` / `cn xt`), auth is staged from the host:

- `~/.codex/auth.json` — the ChatGPT sign-in credentials created by `codex login` on the
  host. Injected read-only and seeded to `/home/dev/.codex/auth.json` (mode 600) only when
  the volume has none yet (rotating refresh tokens — see First-Run Setup; `--reseed-creds`
  forces a re-copy).
- `OPENAI_API_KEY` — forwarded into the container when set in your host environment.

At least one of these must be present. If neither is, `cenci open --agent codex` fails
hard with a clear message and does **not** create a container:

```
Error: --agent codex requires Codex auth. Run 'codex login' on the host
(creates ~/.codex/auth.json) or export OPENAI_API_KEY.
```

If host credentials are not available, open a shell for manual setup:

```bash
cenci open --shell

# Inside the container:
gh auth login              # GitHub CLI auth
claude                     # Claude Code auth (first launch)
claude plugin install ...  # Install any plugins you need
```

For OAuth flows that require a browser callback, use host network mode. Note: this
weakens the container's isolation boundary (the container is the security boundary),
so only use it for the manual OAuth callback:

```bash
cenci open --host-network --shell
# Inside the container, run: claude
```

Everything persists in the home volume — only needs to happen once per instance.

## What's Included

| Tool | Version | Build arg override |
|------|---------|-------------------|
| .NET SDK | 10.0.100 | `DOTNET_SDK_VERSION` |
| Node.js | 24.x | `NODE_MAJOR` |
| Go | 1.24.1 | `GO_VERSION` |
| Playwright | 1.61.1 | `PLAYWRIGHT_VERSION` |
| Codex CLI | 0.144.1 | `CODEX_VERSION` |
| CCometixLine (ccline) | 1.1.2 | `CCLINE_VERSION` |
| GitHub CLI | latest | — |
| git, ripgrep, jq, curl | latest | — |
| build-essential | latest | — |
| Python 3 | latest | — |
| uv | latest | `UV_VERSION` |
| Docker CLI | latest | — |

Override versions at build time. The monolith `Dockerfile` builds `FROM
cenci-sandbox-base:${BASE_VERSION}`, so build (or pull) the base image first and pass
the matching `BASE_VERSION` — `cenci sandbox build` does both steps for you
automatically, resolving `BASE_VERSION` to a content hash of `Dockerfile.base` +
`entrypoint.sh` + `lib/` (see [Two-image model](#two-image-model-base--monolith)
below). For a manual build, `cenci sandbox build-base` always additionally tags
`cenci-sandbox-base:latest`, so a bare `--build-arg BASE_VERSION=latest` works once
any base has been built:

```bash
cenci sandbox build-base   # tags both the content-hash tag and cenci-sandbox-base:latest

docker build --build-arg BASE_VERSION=latest \
             --build-arg DOTNET_SDK_VERSION=10.0.200 \
             --build-arg GO_VERSION=1.25.0 \
             -t cenci-sandbox:latest sandbox/
```

## Architecture

### Two-image model: base + monolith

The image is built in two layers:

- **`Dockerfile.base`** → `cenci-sandbox-base:<content-hash>`, plus an `cenci-sandbox-base:latest`
  alias tag. The hash is a 12-char digest of `Dockerfile.base` + `entrypoint.sh` + `lib/`
  (all its `COPY` inputs), so the base only rebuilds when those actually change — not on
  every plugin.json version bump. Stack-agnostic: Ubuntu 24.04, system packages, locale,
  `uv`, GitHub CLI, Docker CLI, the non-root `dev` user, and the entrypoint. No language
  runtimes. `cenci sandbox build-base` builds it explicitly, and `cenci sandbox build` /
  `cenci open` builds it automatically the first time (or whenever the current content-hash
  tag is missing locally). Run `cenci sandbox prune` to clean up superseded hash tags left
  behind by earlier `Dockerfile.base` changes.
- **`Dockerfile`** → `cenci-sandbox:latest`, `FROM cenci-sandbox-base:${BASE_VERSION}`
  (default `latest`). Layers the runtime stacks on top: .NET SDK, Node.js, Go, Codex CLI
  (ordered last since it changes most often). This is the image `cenci open` actually runs.

`sandbox/fragments/*.dockerfile` holds the same composable blocks (`dotnet`, `node`,
`go`, `python`, `rust`, `codex`) used for per-project image composition. Each fragment and
its corresponding block in `Dockerfile` are kept byte-identical by hand; when you change
one, change the other the same way.

### Per-repo images

A repo can opt into its own thin image instead of the shared monolith by adding
`.cenci/Dockerfile` (and any files it needs, e.g. a fragment copy) under
`.cenci/` at the repo root. When present, the launcher builds
`cenci-sandbox-<repo-slug>:latest` `FROM cenci-sandbox-base:${BASE_VERSION}` — the
same base image and content-hash `BASE_VERSION` as the monolith — using
`.cenci/` as the build context, and runs that instead of `cenci-sandbox:latest`.
Repos without `.cenci/Dockerfile` keep using the shared monolith image, just with
single-repo mounting (see [Per-repo containers](#per-repo-containers)). Rebuild a
repo's own image the same way as the monolith: `cenci sandbox build` (run from inside
that repo).

`/cenci:configure` generates and maintains `.cenci/Dockerfile` automatically
from the repo's detected stack (question 9) — you normally don't hand-write this file.
Every generated image includes the Node and Codex fragments so `cenci open --agent codex`
works in tailored images; it adds the remaining fragments required by the detected stack.
The fragments are wrapped in
`# cenci:managed-begin` / `# cenci:managed-end` markers so re-running configure
regenerates just that block and preserves anything the team appends around it.

**Sync obligation**: `sandbox/fragments/*.dockerfile` is the source of truth for the
per-stack blocks configure assembles into `.cenci/Dockerfile`; the cenci
`configure` skill's stack-to-fragment mapping table mirrors this directory. If a fragment
is added, removed, or renamed here, that table needs a matching manual update — low risk
in practice, since both live in this same monorepo and are maintained together, but
currently unenforced by tooling.

**Trust / security note**: a committed `.cenci/Dockerfile` is reviewed code, like
any other file in the PR that adds or changes it. It only runs `docker build` steps
assembled from `sandbox/fragments/*.dockerfile` by configure's templates — no
arbitrary runtime hooks execute during generation or during the build it produces.

### Permission model

Claude Code runs with `--dangerously-skip-permissions` inside the container: no permission prompts, no tool allowlists. Isolation comes from the container itself, not from Claude Code's permission system. This is the supported use of the flag (it refuses to run as root; the container user is `dev`, UID 1000). Human-in-the-loop control moves up a layer — to workflow gates (plan approval, `AskUserQuestion`) rather than per-command approval.

Codex (`--agent codex`) runs with the direct analog, `--dangerously-bypass-approvals-and-sandbox`: it skips all confirmation prompts and runs commands without Codex's own sandbox, since the flag is intended for externally-sandboxed environments. It is container-safe by the same reasoning as Claude's flag — the container is the security boundary, and we run as `dev`, UID 1000. Unlike Claude's bypass mode there is no persisted "accept" dialog to seed, so the entrypoint does no Codex settings-seeding.

Bypass mode is **fully unattended**. The entrypoint seeds `/home/dev/.claude/settings.json` with `skipDangerousModePermissionPrompt: true` and `permissions.defaultMode: bypassPermissions` (and the image sets `IS_SANDBOX=1`), so even a brand-new `--name` instance on a fresh home volume reaches the prompt with no "Yes, I accept" bypass dialog, and headless `claude -p` runs report `bypassPermissions` instead of silently downgrading to `default`. The settings are deep-merged into any existing file, so unrelated keys survive.

**Security invariant — container-only.** The `skipDangerousModePermissionPrompt` / `defaultMode: bypassPermissions` pair lives *only* in the container home volume (`/home/dev/.claude/settings.json`). It must **never** be added to the host `~/.claude/settings.json`, and the launcher never mounts the host `~/.claude` config dir (staging `.credentials.json` read-only is the single exception). The container boundary is the only thing that makes bypass mode safe — if a dialog ever shows where it shouldn't, the fix is always container-side, never host-side.

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
cenci open --docker
```

This enables:
- **TestContainers**: Integration tests that spin up containers (databases, message brokers, etc.)
- **Docker CLI**: Build images, run containers, use docker compose
- **Any Docker SDK usage**: Libraries that talk to the Docker daemon

The entrypoint automatically detects the socket's group ownership and adds the `dev` user to the matching group.

**Security note**: The `--docker` flag grants the container access to the host's Docker daemon. Any container started from within the sandbox runs on the host, with full Docker privileges. This is why it is opt-in.

### cenci-watch (optional)

The launcher automatically:
- Starts the host daemon when its events socket is missing (it normally starts
  lazily on the first host session, which used to leave containers created
  right after boot without any wiring) and warns if the socket never appears
- Bind-mounts the `cenci` binary (read-only)
- Bind-mounts the events socket directory (read-only) so hooks can reach the
  host daemon — mounting the directory rather than the socket file means the
  wiring survives a host daemon restart, since the container follows the host
  path to the daemon's fresh socket instead of pinning the inode that existed
  at container creation
- Passes `$TMUX_PANE` per exec session (never at container creation, where it
  would land in PID 1's environment and go stale once the creating pane
  closes — #356) for tmux window status updates

A container's mounts are fixed for its whole lifetime. If the shared container
was created while the events socket directory was unavailable, later launches
warn that its sessions won't report to the host status bars; stop the container
(`docker stop <name>`) and relaunch to restore the wiring.

No manual install is needed inside the container. The launcher passes the selected
agent through the internal `CENCI_SANDBOX_AGENT` contract, and the entrypoint uses that
agent's native CLI and plugin store: Claude provisions `~/.claude/plugins` through
the host-mounted `claude` binary, while Codex provisions `~/.codex` through the
Codex CLI baked into the image. Both paths register the `cenci` marketplace,
install `cenci-watch` and `cenci` when missing, and refresh them on a 30-minute
TTL. Rapid stop/start cycles therefore make zero network calls; `cenci sandbox
update-plugins` forces provisioning plus refresh through the selected agent's
CLI. CLI or network failures warn but never block container startup. Existing
Claude home volumes are migrated off the old `muxwatch`/`ccflow` plugins and the
renamed `claude-tools` marketplace at the same time.

Codex validates plugin hook files by hash. A new Codex session loads newly installed
plugins, but if an update changes `hooks.json`, open `/hooks` in Codex and trust the
pending cenci-watch hooks again. This trust decision is intentionally interactive and
is not bypassed by sandbox provisioning.

### Container lifecycle

- Repository containers run detached so no agent window owns their lifetime
- Containers remain available for later launches until stopped or the container runtime restarts
- Containers are created with `--rm` and are removed automatically when stopped
- The home volume survives container removal
- Each `--name` instance gets its own container and volume

Stop a repository container explicitly when you no longer need it, for example:

```bash
docker stop claude-cenci-my-repo
```

## Maintenance

### Update SDK versions

Edit the `ARG` line for the stack you want to bump:

- `DOTNET_SDK_VERSION`, `NODE_MAJOR`, `CODEX_VERSION`, `GO_VERSION` live in `Dockerfile`
  (the monolith layers on top of the base). Stack fragments mirror their corresponding
  pins, including `fragments/codex.dockerfile` for generated per-repo images.
- `UV_VERSION` lives in `Dockerfile.base` — bump it and run `cenci sandbox build-base`
  first, then rebuild the monolith.

Then rebuild:

```bash
cenci sandbox build
```

**Per-repo images too:** a `Dockerfile.base` bump changes `BASE_VERSION` (its content
hash), which every repo's own `.cenci/Dockerfile` image also builds `FROM`. `cenci
sandbox build` only rebuilds the image for the repo you run it in — rebuild each repo
that has opted into `.cenci/Dockerfile` separately (see [Per-repo
images](#per-repo-images)) so it doesn't keep running the stale base.

### Update Claude Code

Just update Claude Code on the host. The binary is bind-mounted, so the container always uses the host version — no rebuild needed.

### Update sandbox plugins

Nothing to do normally: on each container start the entrypoint uses the selected
agent's native CLI to refresh the `cenci` marketplace and update
`cenci`/`cenci-watch` in that agent's home volume (TTL-gated to 30 minutes).
To force provisioning of anything missing and refresh immediately — e.g. right
after merging a plugin change — run:

```bash
cenci sandbox update-plugins                # Claude home / Claude CLI
cenci sandbox update-plugins --agent codex  # Codex home / baked-in Codex CLI
```

It updates the running container in place (agent sessions pick the new version
up on their next start), or spins up a one-shot container against the home
volume if none is running. Codex updates do not require Claude Code to be
installed on the host. After a Codex hook-file update, review pending trust via
`/hooks` in the next Codex session.

### Update Codex

Codex is baked into the image, so updating it means bumping `CODEX_VERSION` in the monolith
and `fragments/codex.dockerfile`, regenerating tailored repo Dockerfiles, and rebuilding
(`cenci sandbox build`). Unlike Claude, updating Codex on the host has no effect on the container.

A scheduled workflow checks for new Codex releases daily, opens a PR bumping
`CODEX_VERSION` in both the monolith and Codex fragment, and auto-merges it — no manual
bumping needed. Run `cenci sandbox build` to pick up the new version in either image type.

### Clean up superseded images and containers

```bash
cenci sandbox prune
```

removes superseded base image tags, dangling images, and stopped sandbox containers —
it keeps the current base tag, `cenci-sandbox-base:latest`, and all per-repo images
untouched. To also list `*-cenci-home-*` volumes and interactively confirm their
removal:

```bash
cenci sandbox prune --volumes
```

Volume deletion defaults to **no** because home volumes hold copied credentials and
your full session history. `--volumes` only means something combined with `prune`; on
its own it errors instead of silently doing nothing.

### Reap orphaned agent processes

```bash
cenci sandbox reap-orphans
CENCI_SANDBOX_REAP_GRACE_SECS=0 cenci sandbox reap-orphans
```

retroactively kills container-side agent processes whose owning tmux pane no longer
exists on the host (SIGTERM, then SIGKILL after a grace period — 5 seconds by default,
override with `CENCI_SANDBOX_REAP_GRACE_SECS`, e.g. `=0` for fast/CI runs). It scans
every running `*-cenci-*` container across all installed runtimes (docker and podman).
If no tmux server is running, every `TMUX_PANE`-carrying process is treated as orphaned
and the output says so explicitly. Processes with a missing/empty `TMUX_PANE` (manual
non-tmux launches) are never signaled, and neither is PID 1 (the container's init,
which carries a stale creation-time `TMUX_PANE` on containers created by older
launchers — killing it would destroy the whole shared container, #356).
Prints one `reaped\t<container>\t<pid>\t<pane>`
line per reaped process plus a final count, and exits non-zero on a genuine runtime
error (e.g. exec failure) rather than swallowing it.

### Reset an instance

Delete the home volume to start fresh (caches, auth, config all cleared). Claude Code
instances use `claude-cenci-home-<repo-slug>`; Codex instances use
`codex-cenci-home-<repo-slug>` (or `-<name>` outside a git repo — see
[Per-repo containers](#per-repo-containers)):

```bash
docker volume rm claude-cenci-home-cenci
# or for a --name instance:
docker volume rm claude-cenci-home-cenci-myproject
# outside a git repo (legacy scheme):
docker volume rm claude-cenci-home-default
# Codex instances:
docker volume rm codex-cenci-home-cenci
```

### List instances

```bash
docker volume ls --filter name=cenci-home
```

### Clean up everything

```bash
# Remove all sandbox volumes (both Claude Code and Codex instances)
docker volume ls --filter name=cenci-home -q | xargs docker volume rm

# Remove the image
docker rmi cenci-sandbox:latest
```

## Sharing the Image

### Via container registry

```bash
docker tag cenci-sandbox:latest ghcr.io/YOUR_ORG/cenci-sandbox:latest
docker push ghcr.io/YOUR_ORG/cenci-sandbox:latest
```

Recipients pull the image and only need the `cenci` binary.

### Via file export

```bash
# Export
docker save cenci-sandbox:latest | gzip > cenci-sandbox.tar.gz

# Import on another machine
docker load < cenci-sandbox.tar.gz
```

## Troubleshooting

**Permission errors on `/workspace` files**
`entrypoint.sh` auto-remaps the container's `dev` user to your host UID/GID on every
launch, so this should no longer happen for the per-repo mount. If you're on the legacy
whole-`~/Repos` mount (outside a git repo) and see stale mis-owned files from before this
fix, run `chown -R $(id -u):$(id -g) ~/Repos` on the host — see
[Limitations](#limitations).

**`claude` binary not found**
Ensure `claude` is in your host PATH. Check with: `readlink -f "$(which claude)"`

**Container runtime**
The launcher auto-detects `podman` first, then falls back to `docker`.

**Claude Code says "request not found" during OAuth**
The OAuth callback can't reach the container. Either:
1. Ensure `~/.claude/.credentials.json` exists on the host (run `claude` on the host first to authenticate), or
2. Use `cenci open --host-network --shell` and run `claude` to complete the OAuth flow with host networking. This weakens the container's isolation boundary, so use it only for this manual OAuth callback.
