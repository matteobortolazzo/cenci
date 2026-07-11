# Getting started

agent-stack looks like three plugins, but it's **one system** — you install it once,
with one command, and update it the same way. This guide takes you from nothing to a
working setup on Linux, macOS, or WSL2.

## What you're installing

| Plugin | Layer | One-line job |
|--------|-------|--------------|
| `agentflow` | workflow | Turns a GitHub ticket into a merged PR, stopping only for *your* decisions (refine, design, plan approval) |
| `agentwatch` | attention | Shows live agent status wherever you're looking — tmux bar, waybar, macOS menu bar — and shouts when the agent needs you |
| `sandbox` | isolation | Runs the agent inside a Docker/Podman container with full permissions, so autopilot is safe |

You can install any subset, but they're designed to work together: the sandbox makes
autopilot safe, agentflow runs the autopilot, agentwatch tells you when it needs you.

## Before you start

The only hard requirements are **[Claude Code](https://code.claude.com/docs/en/overview)**
and **git**. Everything else is per-feature and the installer tells you exactly what's
missing and why it matters:

| You want… | You need… |
|-----------|-----------|
| agentflow (issues & PRs) | [GitHub CLI](https://cli.github.com) (`gh`), authenticated via `gh auth login` |
| sandbox | Docker or Podman (macOS: [Docker Desktop](https://docker.com/products/docker-desktop) or Podman) |
| agentwatch in the tmux status bar | tmux |
| agentwatch in the macOS menu bar | [SwiftBar](https://swiftbar.app) (`brew install swiftbar`) — optional |
| agentflow *without* the sandbox (Linux host profile) | `bubblewrap` + `socat` — skip if you use the sandbox |

Not sure? Run the check first — it changes nothing:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash -s -- doctor
```

## Install

One command. It detects your platform, checks prerequisites, asks which plugins you
want (default: all three), and does the post-install setup that used to be manual:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash
```

Or from a clone:

```bash
git clone https://github.com/matteobortolazzo/agent-stack.git
cd agent-stack && ./install.sh
```

What it does, concretely:

1. Registers this repo as a Claude Code plugin marketplace and, when detected, as a
   Codex plugin marketplace.
2. Installs the selected plugins in Claude Code and Codex. Each client keeps its own
   local plugin cache; the marketplace catalog and plugin sources are shared.
3. **sandbox**: symlinks the `agent-sand` / `codex-sand` launchers into
   `~/.local/bin` and offers to build the container image (a few minutes, one time).
4. **agentwatch**: nothing to do — the binary and daemon self-bootstrap on your first
   Claude Code session. On macOS, if SwiftBar is installed it offers to wire up the
   menu bar widget for you.
5. **agentflow**: checks `gh` auth and points you at the one-time `/agentflow:configure`.

Non-interactive (CI, dotfiles scripts): `bash -s -- --yes --plugins agentflow,agentwatch`.
Run `./install.sh --help` for all flags.

**Codex users — one-time config.** Project instructions live in `CLAUDE.md` files (one per
directory). Claude Code reads them natively; Codex needs one line in its *user-level*
config to discover the same files (a repo-level `.codex/config.toml` is ignored):

```toml
# ~/.codex/config.toml
project_doc_fallback_filenames = ["CLAUDE.md"]
```

<details>
<summary>Prefer to do it by hand?</summary>

```bash
claude plugin marketplace add matteobortolazzo/agent-stack
claude plugin install agentflow agentwatch sandbox

codex plugin marketplace add matteobortolazzo/agent-stack
codex plugin add agentflow@agent-stack
codex plugin add agentwatch@agent-stack
codex plugin add sandbox@agent-stack
```

Then, inside Claude Code, `/sandbox:setup` to symlink the launcher and build the
image. agentwatch needs nothing. For the macOS menu bar widget, see
[agentwatch/plugin/macos/README.md](../agentwatch/plugin/macos/README.md).

</details>

## Your first session

```bash
# 1. Launch Claude Code inside the sandbox (or plain `claude` if you skipped it)
agent-sand

# 2. One-time project setup — detects your stack, writes CLAUDE.md and settings
/agentflow:configure

# 3. Work a ticket end to end
/agentflow:refine 42        # sharpen the ticket together (optional)
/agentflow:implement 42     # plan → your approval → autopilot → open PR
/agentflow:babysit 43       # keep the PR moving: CI fixes + review comments
```

agentwatch needs no commands at all — once a session starts, your tmux window shows
`▶` while the agent works, `✓` when it's done, and `!` (red) when it needs your input.

## Platform notes

### Linux

Everything works natively. If you skip the sandbox and run agentflow on the host, install
`bubblewrap` and `socat` (its host-profile isolation). Waybar/noctalia/DMS widgets for
agentwatch are documented in [agentwatch/README.md](../agentwatch/README.md).

### macOS

- **Sandbox** requires a container runtime — Docker Desktop or Podman. Install one
  before running the image build.
- **agentwatch** works in tmux out of the box. The menu bar widget is a SwiftBar
  plugin; the installer wires it up if SwiftBar is present. One gotcha the installer
  handles for you: SwiftBar's default plugin folder lives under `~/Library`, which
  Finder hides — the installer uses `~/SwiftBarPlugins` instead. Point SwiftBar at it
  in *Preferences → Plugin Folder*.
- **agentflow** host-profile sandboxing is built into Claude Code on macOS — no extra
  packages.

### WSL2

Treated as Linux. Two things to know:

- Docker Desktop for Windows with the WSL2 backend works for the sandbox; make sure
  WSL integration is enabled for your distro.
- Keep your repos on the Linux filesystem (`~/Repos`, not `/mnt/c/...`) — the sandbox
  mounts `~/Repos`, and cross-OS file access is slow anyway.

## Updating

```bash
./install.sh update
# or the same curl one-liner with:  bash -s -- update
```

This updates every installed plugin in each available client, refreshes the launcher
symlinks, and offers to rebuild the sandbox image when needed. AgentWatch
re-bootstraps its matching binary on the next session automatically.

## Renamed from ccflow

The workflow plugin was formerly `ccflow`; it is now **`agentflow`** (matching
`agentwatch`). If you installed the old plugin, migrate once:

```bash
claude plugin uninstall ccflow
claude plugin install agentflow
codex plugin remove ccflow@agent-stack 2>/dev/null || true
codex plugin add agentflow@agent-stack
```

- Skills moved namespace: `/ccflow:*` → `/agentflow:*` (e.g. `/agentflow:refine`,
  `/agentflow:implement`, `/agentflow:babysit`).
- Re-run `/agentflow:configure` to regenerate each project's `.claude/config.json` —
  config keys moved from `ccflow.*` to `agentflow.*`, so the old block is not read.
- If a lazyboards / orchestration board dispatches `/ccflow:*`, switch those column
  actions to the `/agentflow:*` namespace.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `claude: command not found` | Install [Claude Code](https://code.claude.com/docs/en/overview) first — the installer can't do this for you |
| `agent-sand: command not found` after install | `~/.local/bin` isn't on your PATH — add `export PATH="$HOME/.local/bin:$PATH"` to your shell profile |
| No status in tmux after installing agentwatch | The first session bootstraps the binary in the background — give it a moment, then check `${TMPDIR:-/tmp}/agentwatch-bootstrap.log` |
| macOS menu bar item never appears | Usually the SwiftBar GUI-PATH gotcha — see [the SwiftBar README](../agentwatch/plugin/macos/README.md#the-gui-path-gotcha) |
| Sandbox build fails / permission errors on `/workspace` | On Linux your UID must be 1000 (`id -u`); see [dev-sandbox/README.md](../dev-sandbox/README.md#troubleshooting) |
| `/agentflow:*` commands missing in a session | `claude plugin list` should show agentflow; if not, re-run the installer — and note plugins load at session start, so restart Claude Code after installing |
| `agentflow:*` skills missing in Codex | `codex plugin list` should show `agentflow@agent-stack` as installed and enabled; re-run the installer, then start a new Codex session |
| `git push` fails inside the sandbox | SSH remotes don't work through the sandbox network filter — switch to HTTPS: `git remote set-url origin https://github.com/<owner>/<repo>.git` |

## Uninstall

```bash
claude plugin uninstall agentflow agentwatch sandbox
claude plugin marketplace remove agent-stack
codex plugin remove agentflow@agent-stack
codex plugin remove agentwatch@agent-stack
codex plugin remove sandbox@agent-stack
codex plugin marketplace remove agent-stack
rm -f ~/.local/bin/agent-sand ~/.local/bin/codex-sand
# sandbox leftovers, if you built the image:
docker rmi agent-sandbox:latest
docker volume ls --filter name=claude-sand-home -q | xargs -r docker volume rm
# macOS menu bar widget, if wired:
rm -f ~/SwiftBarPlugins/agentwatch.5s.sh
```

## Where to go next

- [Root README](../README.md) — how the three layers fit together, and Codex support
- [agentflow](../agentflow/README.md) — the full pipeline, board lifecycle, babysitting PRs
- [agentwatch](../agentwatch/README.md) — dispatch, auto-pickup, widgets, the Go API
- [dev-sandbox](../dev-sandbox/README.md) — auth injection, Docker-in-Docker, lifecycle
