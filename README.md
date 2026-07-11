# agent-stack

Three layers that let a coding agent implement on autopilot while a human keeps the
decisions — an **isolation** boundary, a **workflow** with human gates, and an
**attention** router that taps you only when a decision is needed. One marketplace,
one install, one update.

## The pitch

```
┌────────────────────────────────────────────────────────┐
│  attention layer   (agentwatch)                        │
│  hooks → daemon → tmux · waybar · noctalia · dms       │
│  "the agent needs YOU" → routed to every surface       │
├────────────────────────────────────────────────────────┤
│  workflow layer    (agentflow)                         │
│  human gates: refine · design · plan approval · AUQ    │
│  autopilot:  /goal-driven phases → PR → CI green       │
│  babysit:    /loop → address-review until merged       │
├────────────────────────────────────────────────────────┤
│  isolation layer   (dev-sandbox)                       │
│  Docker/Podman + full permissions                      │
│  the ONLY security boundary; no prompt friction inside │
└────────────────────────────────────────────────────────┘
```

An optional orchestration board ([lazyboards](https://github.com/matteobortolazzo/lazyboards))
sits on top and dispatches work to these layers — it lives in its own repo.

**Humans decide; the agent implements.** You refine the ticket, reason through the
design, approve the plan, and answer the questions that come up (`AskUserQuestion`).
Everything between those gates runs on autopilot — with *all* permissions, because the
agent is locked inside a container and the container is the only thing standing between
it and your host. Full permissions remove the *mechanical* prompts, not the *decisions*:
the decision model lives one layer up, in the workflow's gates. And because an
autonomous agent working behind a wall is easy to forget, the attention layer taps you
on the shoulder the moment it needs an answer — "the agent needs YOU," on whatever
surface you happen to be looking at. The agent is Claude Code today, Codex when the work
fits it better; the isolation and attention layers don't care which.

## Install

Three plugins, **one installer**. It detects your platform (Linux, macOS, WSL2),
checks prerequisites, walks you through what to install (default: everything), and
does the post-install setup — sandbox launcher + image build, macOS menu bar wiring:

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash
```

```bash
# update everything later:
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash -s -- update

# just check what's missing, change nothing:
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash -s -- doctor
```

New here? Read **[Getting started](./docs/getting-started.md)** — prerequisites per
platform, your first session, troubleshooting, uninstall.

<details>
<summary>Manual install (what the script does)</summary>

```bash
claude plugin marketplace add matteobortolazzo/agent-stack
claude plugin install agentflow agentwatch sandbox
/sandbox:setup   # container layer only — symlink agent-sand + build the image
# later:
claude plugin update --all
```

</details>

Each layer versions independently, but they install and update through this one
mechanism.

## The three layers

### Isolation — [dev-sandbox](./dev-sandbox) (plugin: `sandbox`)

A Docker/Podman container that runs the agent with `--dangerously-skip-permissions` and
mounts only `~/Repos`. It exists so autopilot is *safe*: the container is the single
security boundary, which is what lets every layer above it drop per-command approval
without exposing the host. `/sandbox:setup` symlinks the `agent-sand` launcher and
builds the image.

### Workflow — [agentflow](./agentflow) (plugin: `agentflow`)

The GitHub ticket → PR pipeline, and the home of every human decision gate: interactive
`/agentflow:refine` and `/agentflow:design`, plan approval as the hard stop, and
`AskUserQuestion` from the main agent. Once you approve the plan, `/goal`-driven phases
run unattended to an open PR with green CI, and `/agentflow:babysit` loops on review
comments until the PR merges. It detects the sandbox and drops host-only friction inside it.

### Attention — [agentwatch](./agentwatch) (plugin: `agentwatch`)

An event-driven watcher that turns agent hooks into live status across tmux, waybar,
noctalia, DMS, and the macOS menu bar. Its whole job is to route "the agent needs YOU"
to wherever you're looking, so an agent working autonomously behind the container wall
never waits silently. The plugin self-bootstraps its binary and daemon on first session.

## Agent-agnostic (Claude Code + Codex)

One marketplace serves both agents. Codex deliberately consumes Claude-format plugin
infrastructure: it reads `.claude-plugin/marketplace.json`, accepts
`.claude-plugin/plugin.json` as a manifest, loads `hooks.json`, and sets
`CLAUDE_PLUGIN_ROOT`. So "support both agents" reduces to *one* marketplace, not two —
the same install path above works from Codex.

**Codex `/hooks` trust note.** Codex hash-pins `hooks.json`, so every plugin update that
changes it changes the hash and requires re-trusting the hooks via `/hooks` in Codex.
This is a per-update step for Codex users only.

**One instructions file per directory.** `CLAUDE.md` is canonical, and it lives at the
root of each directory (`CLAUDE.md`, `agentwatch/CLAUDE.md`, `agentflow/CLAUDE.md`) so
both tools discover it. Claude Code reads directory-root `CLAUDE.md` natively. Codex reads
it through its `project_doc_fallback_filenames` config key, which extends Codex's
git-root→cwd discovery walk to extra filenames. That key is only honored from the
*user-level* config (`~/.codex/config.toml`) — a committed repo-level `.codex/config.toml`
is silently ignored — so Codex users add it once:

```toml
# ~/.codex/config.toml
project_doc_fallback_filenames = ["CLAUDE.md"]
```

Discovery is nested: the fallback applies at every level of the walk, so running Codex
inside `agentwatch/` picks up both root `CLAUDE.md` and `agentwatch/CLAUDE.md` (verified
with `codex debug prompt-input`). **Revisit trigger:** once Claude Code reads `AGENTS.md`
natively ([anthropics/claude-code#6235](https://github.com/anthropics/claude-code/issues/6235)),
rename these to `AGENTS.md` — the AAIF/Linux Foundation standard — so Cursor, Copilot, and
Gemini benefit without per-tool config.

Per-layer Codex status is honest about where each layer stands today:

| Layer | Codex today | Roadmap |
|-------|-------------|---------|
| attention (agentwatch) | ✅ watched — self-bootstrapping Codex hooks, `/hooks` trust step | `agentwatch run --agent codex` launch templates ([#33](https://github.com/matteobortolazzo/agent-stack/issues/33)) |
| workflow (agentflow) | Claude Code only | documented `AGENTS.md` equivalent ([#19](https://github.com/matteobortolazzo/agent-stack/issues/19)) |
| isolation (sandbox) | Claude-only launcher | `agent-sand --agent codex` ([#18](https://github.com/matteobortolazzo/agent-stack/issues/18)) |

## License

MIT
