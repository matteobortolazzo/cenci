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

## How a session starts

1. **Resolve the scope.** `cenci open` detects the current git root, selected agent,
   and available container runtime.
2. **Prepare trusted runtime assets.** It builds or reuses the sandbox image and, on
   first use, bootstraps the selected official agent CLI into a shared volume that
   workloads mount read-only.
3. **Start or reuse the repository container.** The repository is mounted at
   `/workspace`; agent configuration lives in an agent- and repository-specific home
   volume so later sessions can resume.
4. **Enter at full permissions.** The launcher execs the agent inside the container.
   No inbound ports are published by default; `--host-network` and `--dind` are explicit
   boundary-widening choices.

The container limits host exposure; it does not make code inside the repository
trusted. The agent can still modify the mounted repository and use outbound network
access. See the [security model](../SECURITY.md) for guarantees and non-goals.

## Prerequisites

- Docker or Podman installed on the host
- Neither agent CLI needs to be installed on the host for container execution: first launch
  bootstraps the selected CLI at its verified official `latest` release into a host-global,
  read-only-at-workload volume (see
  [Choosing an agent](#choosing-an-agent)). You still need each agent's **auth** on the host
  so credentials can be staged into the container.
- Claude auth on the host when using `--agent claude` (the default) — the usual `claude`
  login on the host writes `~/.claude/.credentials.json`, which is staged in read-only.
- Codex auth on the host when using `--agent codex` — run `codex login` to create
  `~/.codex/auth.json`, or export `OPENAI_API_KEY`.
- OpenCode auth on the host when using `--agent opencode` — run `opencode auth login` to
  create `~/.local/share/opencode/auth.json` (staged read-only, mode 600, mirrors Codex), or
  export `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`.

## Installation

The easiest path is the [one-command installer](../docs/getting-started.md), which
installs the plugin, puts the `cenci` binary (and its `cn` launch alias) on your PATH,
and offers to build the image:

```bash
curl -fsSL -o install.sh https://github.com/matteobortolazzo/cenci/releases/latest/download/install.sh
curl -fsSL -o install.sh.bundle https://github.com/matteobortolazzo/cenci/releases/latest/download/install.sh.bundle
cosign verify-blob --bundle install.sh.bundle \
  --certificate-identity-regexp '^https://github\.com/matteobortolazzo/cenci/\.github/workflows/watch-release\.yml@refs/(heads/main|tags/watch/v[0-9]+\.[0-9]+\.[0-9]+)$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  install.sh
bash install.sh
```

Requires [cosign](https://docs.sigstore.dev/system_config/installation/) — the installer
verifies its own bytes against the release before running, and fails closed with no
fallback to an unverified ref. The legacy one-liner still works and re-execs itself
through this same verified path:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/cenci/main/install.sh | bash
```

Set `CENCI_REF=main` (or pass `--ref main`) to explicitly opt into bleeding-edge,
unverified main instead (unsafe; development use only).

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
cenci open --agent opencode --name mybox     # OpenCode (no cenci-side shortcuts yet)

# Pass args through to the agent CLI — everything after a bare -- is forwarded verbatim
cn -- -p "fix the tests"
cn cs -- --resume

# Open a bash shell for manual setup / troubleshooting
cenci open --shell

# Build / maintain the images
cenci sandbox build   # (re)build the image
cenci sandbox prune             # clean up superseded base tags, dangling images, stopped sandbox containers
cenci sandbox prune --images    # …and prompt ([y/N], default deny) to remove per-repo images too
```

A rebuild never touches already-running containers — a container's image is
fixed at create time — so an existing session keeps running the old image
until it's stopped and relaunched. `cenci sandbox build` names any running
sandboxes still on the old image so you know which ones need it.

This is deliberately just a taste: the full launcher reference — every `cenci open`
flag (`--agent`, `--model`, `--name`, `--shell`, `--dind`, `--no-dind`, `--host-network`,
`--reseed-creds`), the `cenci sandbox` verbs (`build`, `build-base`, `prune`,
`update-agent`, `update-plugins`, `reseed-creds`, `reap-orphans`, `ls`, `stop`), the shortcut table,
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
`xl`/`xt`/`xs` shortcut) to launch Codex instead, or `--agent opencode` to launch OpenCode
(no cenci-side shortcuts for OpenCode yet). All three agents run at full permission
inside the container — Claude with `--dangerously-skip-permissions`, Codex with
`--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust`. OpenCode has no
equivalent CLI flag; its permissions are config-driven via a seeded `opencode.json` instead —
see [Permission model](#permission-model).

Containers and home volumes are **namespaced by agent**, so the three never collide:
the **Claude agent** uses the `claude-cenci-` prefix; the **Codex agent** (`--agent
codex` / `cn xt`) uses `codex-cenci-`; the **OpenCode agent** (`--agent opencode`) uses
`opencode-cenci-`. The rest of the name is the repo slug (or the legacy `<name>`
outside a git repo — see [Per-repo containers](#per-repo-containers) above), e.g.
`claude-cenci-my-project` / `claude-cenci-home-my-project`. The executable itself is shared
across every repository and named instance on the host in `cenci-agent-cli-claude`,
`cenci-agent-cli-codex`, or `cenci-agent-cli-opencode`, mounted read-only at
`/opt/cenci-agent`. Sessions invoke its absolute path, so an old or tampered executable in a
home volume cannot shadow it.

When the shared volume is absent, the launcher resolves an exact official version and SHA-512
integrity, then installs it in a short-lived updater with no repository, home, credentials,
API keys, host network, or container socket. Registry signatures and available npm provenance
are verified before required postinstall code runs there. Codex provenance must identify
`openai/codex`; Claude Code and OpenCode currently publish registry signatures but no npm
provenance, so their remaining trust boundary is the vendor's legitimate npm release
authority. A malicious release legitimately published by a vendor cannot be prevented by
package integrity alone.

Updates use a volume-scoped `flock`, versioned staging, a `--version` health check, and an
atomic `current` symlink. The previous release is retained; a failed or interrupted update
leaves the active release untouched. Launches do no network or version check after bootstrap.
Native update flows are disabled because workload mounts cannot modify the CLI. Explicit
updates are global across repositories. Running containers from the older writable-home
lifecycle must be stopped and relaunched before attachment.

**`update-agent` mutates a host-global volume shared by every sandbox** — not just this
repo's — including pinning or downgrading via `--version`. Every repository and named
instance on the host resolves the same `current` symlink.

**Interim pin behavior:** `--version` now writes a persistent pin to the shared volume, and
a subsequent bare `update-agent` (no `--version`) refuses rather than silently reinstalling
`latest`. Until an `--unpin` flag lands, the only way to clear the pin is running
`agent-cli.sh unpin <agent>` directly inside the updater container.

**Version-retention race:** only the current and previous releases are kept on disk. An
already-running sandbox holds its agent CLI open from the release directory it started
with, so two `update-agent` calls in a row (while that session is still running) can prune
the release out from under it — the version currently executing is neither "current" nor
"previous" once two newer updates have landed. `update-agent`'s own output notes this:
already-running sandboxes keep using the version they started with and must be relaunched
to pick up an update, but a session spanning two or more updates can hit this race. This
is a known limitation, not a bug to work around by widening retention.

## First-Run Setup

If `~/.claude/.credentials.json` and `~/.config/gh/hosts.yml` exist on the host, they are automatically injected into the container. **No manual auth needed** — with one exception for the GitHub CLI, see [GitHub CLI auth](#github-cli-auth) below.

Claude (and Codex) OAuth uses rotating refresh tokens: after the sandbox's first
token refresh, the volume's credentials become an independent login from the
host's — like being signed in on two machines. Claude and Codex credentials are
therefore **seeded only when the volume has none yet** and never overwritten on
later starts: each instance stays logged in indefinitely, and using the sandbox
all day can no longer log your host session out (you may see one final host
re-login right after a volume is first seeded, then both sides are stable). The
GitHub CLI token doesn't rotate, so `hosts.yml` is still refreshed from the host
on every start — as long as it actually carries the token, see
[GitHub CLI auth](#github-cli-auth). OpenCode's `auth.json` (when present) goes
through the same seed-once staging path — see
[OpenCode auth](#opencode-auth) below.

If an instance's login does die (e.g. you revoked all sessions on claude.ai),
force a one-time re-copy from the host:

```bash
cenci open --reseed-creds
# or the maintenance-verb alias:
cenci sandbox reseed-creds
```

### GitHub CLI auth

`gh auth login` defaults to **secure storage**: the token goes into the host OS
keyring and `~/.config/gh/hosts.yml` records only the account name. The
container has no access to a host keyring — there is no secret-service provider
in the image, and starting a session bus doesn't supply one.

A token-less `hosts.yml` is worse than none at all: gh's multi-account config
migration finds an account whose token it can't resolve and aborts, so **every**
gh command fails — including `gh --version`, and including runs with `GH_TOKEN`
set, because the migration happens on config load, before auth resolution. The
error names dbus and reads like a missing package, which it isn't:

```
failed to migrate config: cowardly refusing to continue with multi account
migration: couldn't find oauth token for "github.com": dbus: couldn't determine
address of session bus
```

So the sandbox **skips** a token-less `hosts.yml` rather than copying it in, and
warns on startup. gh then starts clean and honours `GH_TOKEN`. To get a real
login, either store the token in the file on the host and re-open the sandbox:

```bash
gh auth login --insecure-storage   # on the host; writes oauth_token into hosts.yml
```

…or log in inside the container:

```bash
gh auth login                      # inside the sandbox
```

An in-container login persists in the home volume and is no longer overwritten
by a token-less host file on the next start. A host file that *does* carry a
token still wins, so host re-auths keep propagating as before.

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
- `OPENAI_API_KEY` — forwarded per agent session (`exec`) when set in your host
  environment; never baked into the container's create-time environment.

At least one of these must be present. If neither is, `cenci open --agent codex` fails
hard with a clear message and does **not** create a container:

```
Error: --agent codex requires Codex auth. Run 'codex login' on the host
(creates ~/.codex/auth.json) or export OPENAI_API_KEY.
```

### OpenCode auth

When launching OpenCode (`--agent opencode`), auth is staged from the host:

- `~/.local/share/opencode/auth.json` — the subscription/OAuth sign-in credentials created
  by `opencode auth login` on the host. Injected read-only and seeded to
  `/home/dev/.local/share/opencode/auth.json` (mode 600) only when the volume has none yet
  (same seed-once staging as Codex — see First-Run Setup; `--reseed-creds` forces a
  re-copy).
- `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` — forwarded into the container when set in your
  host environment (OpenCode reads these natively; like Codex's `OPENAI_API_KEY`, neither is
  baked into the container's create-time environment, only passed per-`exec`).

At least one of these must be present. If neither is, `cenci open --agent opencode` fails
hard with a clear message and does **not** create a container:

```
Error: --agent opencode requires OpenCode auth. Run 'opencode auth login' on the host
(creates ~/.local/share/opencode/auth.json) or export ANTHROPIC_API_KEY/OPENAI_API_KEY.
```

The launcher itself makes no assumption about the host OpenCode version — see
[cenci-watch's OpenCode adapter section](../watch/README.md#dispatching-workflows-cenci-run)
for the pinned minimum version (`cenci-installer doctor` enforces it) and its known
limitations.

### Pencil auth (headless design reads)

`/cenci:design` itself never runs in-container — the Pencil desktop app it drives is
never reachable from inside the cenci sandbox, so the skill fails fast with
host-session guidance rather than attempting the headless CLI (see
`flow/skills/design/SKILL.md` Phase 0.5). What this section documents is narrower:
the headless reads the *pipeline* performs (`implement`, `verify-ui`) once a design
already exists.

Repos with Pencil design workflows enabled (`pencil.enabled` in `.cenci/config.json`)
run their `implement`/`verify-ui` design reads inside the sandbox through
`@pen.dev/cli`'s **headless** mode (`pen interactive -i <design>.pen`) — the CLI
runs the full editor engine with local rendering, so no desktop app, GUI, or MCP
connection is needed. The CLI itself is baked into the per-repo image by
`fragments/pencil.dockerfile` (selected by `/cenci:configure`); auth is optional
staging, never a hard launch requirement:

- `~/.pencil/session-cli.json` — the session created by `pen login` on the host.
  Injected read-only and seeded to `/home/dev/.pencil/session-cli.json` (mode 600) only
  when the volume has none yet (same seed-once staging as the agent credentials;
  `--reseed-creds` forces a re-copy).
- `PEN_CLI_KEY` — an org-scoped CLI key (created under Developer Keys in the Pencil web
  app). Forwarded per agent session (`exec`) when set in your host environment; never
  baked into the image or the container's create-time environment. Takes precedence
  over a seeded session inside the CLI.

With neither present, the pipeline degrades gracefully: the Pencil availability probe
fails and implement proceeds with `DESIGN.md` text context only.

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

| Tool | Version | Override / update |
|------|---------|-------------------|
| .NET SDK | 10.0.100 | `DOTNET_SDK_VERSION` |
| Node.js | 24.x | `NODE_MAJOR` |
| Go | 1.24.1 | `GO_VERSION` |
| Playwright | 1.61.1 | `PLAYWRIGHT_VERSION` |
| Codex CLI | verified latest in shared volume | `cenci sandbox update-agent --agent codex [--version X.Y.Z]` |
| Claude Code CLI | verified latest in shared volume | `cenci sandbox update-agent [--version X.Y.Z]` |
| OpenCode CLI | verified latest in shared volume | `cenci sandbox update-agent --agent opencode [--version X.Y.Z]` |
| CCometixLine (ccline) | 1.1.2 | `CCLINE_VERSION` |
| GitHub CLI | latest | — |
| git, ripgrep, jq, curl | latest | — |
| build-essential | latest | — |
| Python 3 | latest | — |
| uv | latest | `UV_VERSION` |
| Docker CLI + engine | latest | — (monolith and `dind`-enabled per-repo images only) |

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
  `uv`, GitHub CLI, the non-root `dev` user, and the entrypoint. No language
  runtimes, and — since #831 — no Docker CLI or engine either: those moved to the
  config-selected `fragments/docker.dockerfile` so only images that actually run nested
  Docker carry them. `cenci sandbox build-base` builds it explicitly, and `cenci sandbox build` /
  `cenci open` builds it automatically the first time (or whenever the current content-hash
  tag is missing locally). Run `cenci sandbox prune` to clean up superseded hash tags left
  behind by earlier `Dockerfile.base` changes.
- **`Dockerfile`** → `cenci-sandbox:latest`, `FROM cenci-sandbox-base:${BASE_VERSION}`
  (default `latest`). Layers the runtime stacks on top: .NET SDK, Node.js, Playwright, and
  Go. This is the image `cenci open` actually runs; agent CLIs live in shared volumes.

`sandbox/fragments/*.dockerfile` holds the same composable blocks (`dotnet`, `node`,
`playwright`, `go`, `python`, `rust`) used for per-project image composition. Each fragment and
its corresponding block in `Dockerfile` are kept byte-identical by hand; when you change
one, change the other the same way.

### Per-repo images

A repo can opt into its own thin image instead of the shared monolith by adding
`.cenci/Dockerfile` (and any files it needs, e.g. a fragment copy) under
`.cenci/` at the repo root. When present, the launcher builds
`cenci-sandbox-<repo-slug>:latest` `FROM cenci-sandbox-base:${BASE_VERSION}` — the
same base image and content-hash `BASE_VERSION` as the monolith — using
`.cenci/` as the build context, and runs that instead of `cenci-sandbox:latest`. The
built image records the base it was built against in a `cenci.base-version` label, so
if the base later drifts, `cenci open` / `cenci sandbox build` detect the mismatch and
self-heal by rebuilding automatically. Repos without `.cenci/Dockerfile` keep using
the shared monolith image, just with single-repo mounting (see [Per-repo
containers](#per-repo-containers)). Rebuild a repo's own image the same way as the
monolith: `cenci sandbox build` (run from inside that repo). Per-repo images have no
automatic expiry, so they accumulate on the host
as repos come and go — run `cenci sandbox prune --images` to prompt ([y/N], default
deny) for removal of every `cenci-sandbox-<slug>:latest` image found; each repo's
image is removed individually (best-effort), so one repo's sandbox holding its image
open never blocks cleanup of another repo's image. `prune` has no repo/cwd scope of
its own, so it always lists every candidate on the host rather than trying to detect
which repos still exist on disk.

`/cenci:configure` generates and maintains `.cenci/Dockerfile` automatically
from the repo's detected stack (question 9) — you normally don't hand-write this file.
Every generated image includes the Node fragment so the isolated updater can install either
npm-distributed agent; it adds the remaining fragments required by the detected stack.
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

**Stale managed block:** a repo whose committed `.cenci/Dockerfile` still contains an
older `cenci:managed-begin`/`cenci:managed-end` block from before agent CLIs moved to
shared volumes keeps baking a stale, root-owned `codex` (or `claude`) binary into
`/usr/local/bin` on every image build. `cenci open` launches are unaffected — the
launcher always execs the shared volume's absolute path — but an interactive shell in
that repo's container may still see the frozen, image-baked version if it shadows the
shared one on `PATH`. Rerun `/cenci:configure` to regenerate the managed block and drop
the stale binary.

### Permission model

Claude Code runs with `--dangerously-skip-permissions` inside the container: no permission prompts, no tool allowlists. Isolation comes from the container itself, not from Claude Code's permission system. This is the supported use of the flag (it refuses to run as root; the container user is `dev`, UID 1000). Human-in-the-loop control moves up a layer — to workflow gates (plan approval, `AskUserQuestion`) rather than per-command approval.

Codex (`--agent codex`) runs with the direct analog, `--dangerously-bypass-approvals-and-sandbox`: it skips all confirmation prompts and runs commands without Codex's own sandbox, since the flag is intended for externally-sandboxed environments. It is container-safe by the same reasoning as Claude's flag — the container is the security boundary, and we run as `dev`, UID 1000. Unlike Claude's bypass mode there is no persisted "accept" dialog to seed, so the entrypoint does no Codex settings-seeding. The launcher also passes `--dangerously-bypass-hook-trust`: Codex pins hook trust in the user config layer and offers no non-interactive way to seed it, so without the flag the provisioned cenci-watch hooks would sit "pending review" forever and sandbox sessions would never report to the watch daemon. Container-safe for the same reason — the only hooks in the volume are the ones the sandbox itself provisions.

Bypass mode is **fully unattended**. The entrypoint seeds `/home/dev/.claude/settings.json` with `skipDangerousModePermissionPrompt: true` and `permissions.defaultMode: bypassPermissions` (and the image sets `IS_SANDBOX=1`), so even a brand-new `--name` instance on a fresh home volume reaches the prompt with no "Yes, I accept" bypass dialog, and headless `claude -p` runs report `bypassPermissions` instead of silently downgrading to `default`. The settings are deep-merged into any existing file, so unrelated keys survive.

**Security invariant — container-only.** The `skipDangerousModePermissionPrompt` / `defaultMode: bypassPermissions` pair lives *only* in the container home volume (`/home/dev/.claude/settings.json`). It must **never** be added to the host `~/.claude/settings.json`, and the launcher never mounts the host `~/.claude` config dir (staging `.credentials.json` read-only is the single exception). The container boundary is the only thing that makes bypass mode safe — if a dialog ever shows where it shouldn't, the fix is always container-side, never host-side.

OpenCode (`--agent opencode`) has no per-flag "skip permissions" equivalent to bypass with — the CLI has no `--dangerously-*` flag of its own. Instead the entrypoint seeds `/home/dev/.config/opencode/opencode.json` with a `permission: {"*": "allow"}` block, plus `autoupdate: false` (workload mounts are read-only, so native update checks would only fail). Both are seeded **only when the corresponding key is absent** — a user who already set a `permission` block (possibly stricter than the container-boundary default) or explicitly opted back into `autoupdate` keeps their own choice; any other existing key in the file is left untouched.

**Security invariant — container-only.** Exactly like Claude's bypass settings above, the seeded `permission: {"*": "allow"}` block lives *only* in the container home volume (`/home/dev/.config/opencode/opencode.json`). It must **never** be added to a host `~/.config/opencode/opencode.json`, and the launcher never mounts a host OpenCode config dir. The container boundary is what makes the allow-all permission block safe.

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
| `/home/dev/.local/share/opencode/` | OpenCode auth |
| `/home/dev/.config/opencode/` | OpenCode config (`opencode.json`), plugins |
| `/home/dev/.npm/` | npm package cache |
| `/home/dev/.nuget/` | NuGet package cache |
| `/home/dev/.dotnet/` | .NET user-level config |
| `/home/dev/go/` | Go modules and build cache |
| `/home/dev/.config/gh/` | GitHub CLI auth token |
| `/home/dev/.bash_history` | Shell history |

### What's bind-mounted read-only

| Host path | Container path | Purpose |
|-----------|---------------|---------|
| `~/.config/git/config` or `~/.gitconfig` | `/home/dev/.gitconfig` | Git identity |
| `~/.claude/.credentials.json` | `/tmp/host-claude-creds/` (staging) | Claude OAuth tokens (copied to home on start) |
| `~/.codex/auth.json` (Codex only) | `/tmp/host-codex-creds/` (staging) | Codex OAuth tokens (copied to home on start) |
| `~/.local/share/opencode/auth.json` (OpenCode only) | `/tmp/host-opencode-creds/` (staging) | OpenCode OAuth tokens (copied to home on start) |
| `~/.config/gh/hosts.yml` | `/tmp/host-gh-config/` (staging) | GitHub CLI tokens (copied to home on start, only when the file carries an `oauth_token` — see [GitHub CLI auth](#github-cli-auth)) |
| `~/.pencil/session-cli.json` | `/tmp/host-pencil-creds/` (staging) | Pencil CLI session for headless design reads (copied to home on start) |

### MCP servers

MCP servers are picked up from project-scoped `.mcp.json` files inside the workspace (e.g. `./.mcp.json` under the project you're working on). The launcher forwards `CONTEXT7_API_KEY` from the host when set, so `.mcp.json` entries referencing `${CONTEXT7_API_KEY}` resolve correctly inside the container.

### Nested Docker (sysbox)

Repos that need Docker inside the sandbox — Testcontainers, `docker build`/`docker run`
in tests, or any Docker SDK client — can enable **dind mode**: an inner `dockerd` runs
*inside* the sandbox container, isolated by the [sysbox](https://github.com/nestybox/sysbox)
OCI runtime (`sysbox-runc`). This replaces the retired `--docker` flag's
Docker-outside-of-Docker (DooD) socket mount, which gave the sandbox direct access to the
host's Docker daemon.

**How it works**: the launcher starts the outer sandbox container with
`--runtime=sysbox-runc` and `-e CENCI_SANDBOX_DIND=1`. Sysbox gives that container its own
kernel-level container/VM nesting capability, so `entrypoint.sh` starts a private `dockerd`
inside it, backed by a dedicated volume — never the host's Docker daemon or socket. This
isolation relies on sysbox's user-namespace separation between the inner and outer
daemons, and the inner `dockerd` is itself an additional daemon and attack surface versus a
sandbox with no Docker at all — enable dind only for repos that genuinely need
in-container Docker; `--no-dind` remains the escape hatch (below) when they don't.

**Host install** (one-time, per machine): dind mode requires a **Linux** host running
**Docker** as the outer runtime (not Podman — sysbox-runc is a Docker-only OCI runtime)
with `sysbox-runc` registered. `cenci doctor` reports whether it's registered.
- **Arch Linux**: AUR package `sysbox-ce`, e.g. `yay -S sysbox-ce`
- **Ubuntu**: download the nestybox `sysbox-ce` `.deb` from
  [github.com/nestybox/sysbox/releases](https://github.com/nestybox/sysbox/releases) and
  install it with `sudo apt install ./sysbox-ce_*.deb`
- Docs: [github.com/nestybox/sysbox](https://github.com/nestybox/sysbox)

**Enabling it**: set `"sandbox": { "dind": true }` in `.cenci/config.json` (written by
`/cenci:configure`'s dind question) to enable it by default for a repo, or override per
launch with `cenci open --dind` (force on) / `cenci open --no-dind` (force off — always
works as an escape hatch, even if the repo config has `dind: true`). Combining `--dind` and
`--no-dind` is a usage error.

**Image requirement**: the inner daemon needs the Docker CLI, `docker-ce` and
`containerd.io` *in the image*. These are not in `Dockerfile.base` — since #831 they live
in the config-selected `fragments/docker.dockerfile`, so images that never run nested
Docker do not carry the engine. Two cases:
- **No `.cenci/Dockerfile`** — the repo launches the shared `cenci-sandbox:latest`
  monolith, which includes the Docker block. Nothing to do.
- **A per-repo `.cenci/Dockerfile`** — it must include the docker fragment.
  `/cenci:configure` adds it whenever `sandbox.dind` is true. A repo whose Dockerfile was
  generated before #831 (or without the fragment) still boots and stays usable, but has no
  `dockerd`: `entrypoint.sh` writes `~/.cenci-dockerd-startup-error` naming the cause.
  Fix it by re-running `/cenci:configure` and then `cenci sandbox build`.

**Volume lifecycle**: each repo gets its own persistent Docker storage volume, named
`<agent>-cenci-dind-<slug>[-<name>]` and mounted at `/var/lib/docker` inside the container —
so image layers and containers built inside the sandbox survive across sessions for that
repo. `cenci sandbox prune --volumes` includes dind volumes in its stale-volume cleanup
alongside home and agent-CLI volumes.

**Limitations**:
- **Linux-only host**: sysbox is a Linux-only OCI runtime that must be installed on the
  machine running `dockerd`. On macOS `dockerd` lives inside Docker Desktop's LinuxKit VM,
  which cannot be modified, so `sysbox-runc` can never be registered there. Rather than
  refusing to launch (which left macOS unable to open a sandbox at all for a dind repo),
  the launcher **degrades**: it prints a warning naming `CENCI-SANDBOX-DIND-002` and starts
  the session without nested Docker, so everything that doesn't need Docker still works.
  `cenci audit` reports the dind source as `platform-unsupported` on such a host. Pass
  `--no-dind`, or set `"dind": false`, to silence the warning. Getting real nested Docker
  on a Mac means running Docker Engine inside a Linux VM you control (Lima, Multipass) with
  `sysbox-ce` installed there and `DOCKER_HOST` pointed at it — cenci does not manage that
  VM. On Linux, an unregistered `sysbox-runc` is a fixable setup gap and still hard-fails
  the launch with the install pointers above.
- **Docker-outer-only**: dind requires the host's resolved container runtime to be Docker; it is not supported when the outer runtime is Podman.
- **Repo-scope-only**: dind is only available when launching from inside a git repo (repo scope) — not in legacy/default scope.
- **Self-skips in CI**: installing sysbox on a CI runner is out of scope for cenci; CI environments simply don't have it registered, so dind-dependent tests should not assume it's available there.

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

No manual install is needed inside the container. The launcher bootstraps an absent shared
CLI volume before creating the long-lived workload and passes the selected agent through the
internal `CENCI_SANDBOX_AGENT` contract. Updater diagnostics stream directly; failure stops
before credentials or a workload container are involved. Claude then
provisions `~/.claude/plugins` and Codex provisions `~/.codex` through the writable CLI.
Both paths register the `cenci` marketplace,
install `cenci-watch` and `cenci` when missing, and refresh them on a 30-minute
TTL. Rapid stop/start cycles therefore make zero network calls; `cenci sandbox
update-plugins` forces provisioning plus refresh through the selected agent's
CLI. Plugin network failures warn but never block container startup. Existing
Claude home volumes are migrated off the old `muxwatch`/`ccflow` plugins and the
renamed `claude-tools` marketplace at the same time.

Codex validates plugin hook files by hash. A new Codex session loads newly installed
plugins, but if an update changes `hooks.json`, open `/hooks` in Codex and trust the
pending cenci-watch hooks again. This trust decision is intentionally interactive and
is not bypassed by sandbox provisioning.

OpenCode has no marketplace CLI like `claude plugin marketplace add` / `codex plugin
marketplace add`, so provisioning uses the analogous mechanism instead: a plain `git clone`
of `matteobortolazzo/cenci` into `/home/dev/.cenci-src`, giving
`PLUGIN_ROOT=/home/dev/.cenci-src/flow` for `flow/opencode/install-skills.sh` (the primitive
that symlinks the portable skills into `~/.config/opencode/skills/`). The clone happens once;
`cenci sandbox update-plugins --agent opencode` (or the 30-minute TTL) instead `git pull`s the
existing clone and re-runs `install-skills.sh` to link anything newly portable. Every step
warns and never blocks container start if `git` is missing or the clone/pull fails offline.

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

- `DOTNET_SDK_VERSION`, `NODE_MAJOR`, `GO_VERSION` live in `Dockerfile`
  (the monolith layers on top of the base). Stack fragments mirror their corresponding
  pins.
- `UV_VERSION` lives in `Dockerfile.base` — bump it and run `cenci sandbox build-base`
  first, then rebuild the monolith.
- Agent CLIs are not image dependencies and have no build args; update them separately as
  described below.

Then rebuild:

```bash
cenci sandbox build
```

**Per-repo images too:** a `Dockerfile.base` bump changes `BASE_VERSION` (its content
hash), which every repo's own `.cenci/Dockerfile` image also builds `FROM`. `cenci
sandbox build` only rebuilds the image for the repo you run it in, so you still need to
run it from inside each repo that has opted into `.cenci/Dockerfile` (see [Per-repo
images](#per-repo-images)) to trigger that repo's rebuild. You no longer have to
remember to do this proactively, though: the built-in `cenci.base-version` label lets
`cenci open` / `cenci sandbox build` detect that a repo's image was built against an
older base, print a notice, and rebuild it automatically on the next run.

### Update an agent CLI

Workload mounts are read-only, so native Claude Code, Codex, and OpenCode update checks are
suppressed. Update the host-global agent volume explicitly:

```bash
cenci sandbox update-agent                                  # Claude, official latest
cenci sandbox update-agent --agent codex                    # Codex, official latest
cenci sandbox update-agent --agent codex --version 1.2.3    # exact rollback/rollout
cenci sandbox update-agent --agent opencode                 # OpenCode, official latest
```

`--version` accepts only an exact semantic version; ranges and tags are rejected. The command
uses the same isolated verification and atomic activation path as bootstrap. It is global:
new and existing sessions in every repository see the activated version through their shared
read-only mount. Updating the cenci binary or rebuilding an image does not update either CLI.

### Update sandbox plugins

Nothing to do normally: on each container start the entrypoint uses the selected
agent's native CLI to refresh the `cenci` marketplace and update
`cenci`/`cenci-watch` in that agent's home volume (TTL-gated to 30 minutes).
To force provisioning of anything missing and refresh immediately — e.g. right
after merging a plugin change — run:

```bash
cenci sandbox update-plugins                  # Claude home / Claude CLI
cenci sandbox update-plugins --agent codex    # Codex home / writable Codex CLI
cenci sandbox update-plugins --agent opencode # OpenCode home / cenci-src git clone
```

It updates the running container in place (agent sessions pick the new version
up on their next start), or spins up a one-shot container against the home
volume if none is running. Codex and OpenCode updates do not require Claude Code to be
installed on the host. After a Codex hook-file update, review pending trust via
`/hooks` in the next Codex session.

### Clean up superseded images and containers

```bash
cenci sandbox prune
```

removes superseded base image tags, dangling images, and stopped sandbox containers —
it keeps the current base tag, `cenci-sandbox-base:latest`, and all per-repo images
untouched. To also list credential-bearing `*-cenci-home-*` volumes and non-credential
`cenci-agent-cli-*` volumes, clearly separated, and interactively confirm their removal:

```bash
cenci sandbox prune --volumes
```

Volume deletion defaults to **no** because home volumes hold copied credentials and
your full session history. Shared CLI volumes hold no credentials, but deleting one makes the
next launch bootstrap it again. `--volumes` only means something combined with `prune`; on
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
`codex-cenci-home-<repo-slug>`; OpenCode instances use `opencode-cenci-home-<repo-slug>`
(or `-<name>` outside a git repo — see
[Per-repo containers](#per-repo-containers)):

```bash
docker volume rm claude-cenci-home-cenci
# or for a --name instance:
docker volume rm claude-cenci-home-cenci-myproject
# outside a git repo (legacy scheme):
docker volume rm claude-cenci-home-default
# Codex instances:
docker volume rm codex-cenci-home-cenci
# OpenCode instances:
docker volume rm opencode-cenci-home-cenci
```

### List instances

```bash
docker volume ls --filter name=cenci-home
```

### Clean up everything

```bash
# Remove all sandbox volumes (Claude Code, Codex, and OpenCode instances)
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

## Known limitations

**Legacy `~/Repos` mount and pre-existing files.** The container's `dev` user is baked
in at UID/GID 1000, but `entrypoint.sh` auto-remaps it to your host `HOST_UID`/`HOST_GID`
on every launch (the launcher passes them in), so files newly written into the per-repo
`/workspace` mount always come out owned by your host user—no manual `chown` needed.
Renumbering a live account requires no process running under it yet, so the container
briefly starts as `root` for this remap step before `entrypoint.sh` unconditionally drops
privileges to the host-remapped `dev` user.

The remaining caveat is the legacy whole-`~/Repos` mount used outside a git repo. The
remap does not retroactively `chown` that tree because rewriting ownership across the
entire directory is too large a blast radius to automate. If it contains stale,
mis-owned files from an older cenci version, run
`chown -R $(id -u):$(id -g) ~/Repos` on the host.

## Troubleshooting

**Permission errors on `/workspace` files**
`entrypoint.sh` auto-remaps the container's `dev` user to your host UID/GID on every
launch, so this should no longer happen for the per-repo mount. If you're on the legacy
whole-`~/Repos` mount (outside a git repo) and see stale mis-owned files from before this
fix, run `chown -R $(id -u):$(id -g) ~/Repos` on the host — see
[Known limitations](#known-limitations).

**`claude`, `codex`, or `opencode` not found inside the container**
The selected CLI is executed from `/opt/cenci-agent/current`. Check bootstrap diagnostics for
registry, signature, provenance, or network errors. To repair or refresh the shared global
volume, run `cenci sandbox update-agent --agent claude|codex|opencode`. If the volume is still
bootstrapping or a previous update left it broken, the entrypoint now fails the container
startup itself with a one-line diagnostic (rather than a raw `exec: no such file` error);
`cenci open` surfaces that diagnostic directly.

**Interactive shell runs an old/frozen `claude` or `codex`**
If `~/Repos/<repo>/.cenci/Dockerfile` still has an old managed block from before agent CLIs
moved to shared volumes, its image build bakes a stale, root-owned copy into
`/usr/local/bin`. See [Per-repo images](#per-repo-images) — rerun `/cenci:configure` to
regenerate the managed block and remove it.

**Container runtime**
The launcher auto-detects `podman` first, then falls back to `docker`.

**Claude Code says "request not found" during OAuth**
The OAuth callback can't reach the container. Either:
1. Ensure `~/.claude/.credentials.json` exists on the host (run `claude` on the host first to authenticate), or
2. Use `cenci open --host-network --shell` and run `claude` to complete the OAuth flow with host networking. This weakens the container's isolation boundary, so use it only for this manual OAuth callback.
