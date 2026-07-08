# Proposal: one cohesive package

Status: accepted · 2026-07-08 (decisions in §4)

The repo today ships three good tools with three unrelated install stories and no shared
security model. This proposal turns them into one package with a single principle:

> **The container is the security boundary. The workflow owns the decisions.
> The watcher routes your attention.**

Humans decide (refine, design, approve the plan, answer questions). Claude implements
on autopilot — with *all* permissions, because it is locked in a container, and with
`/goal` keeping it going until the work is actually done.

## 1. Review: where the seams are

| # | Gap | Detail |
|---|-----|--------|
| 1 | **Two competing security models** | ccflow is built around Claude Code's *host* sandbox (bubblewrap, `allowedDomains`, Bash allowlists, deny rules) while dev-sandbox isolates via Docker with a tool allowlist. Running ccflow inside the sandbox means double isolation: SSH-push failures, `go` env workarounds, permission auto-fix prompts — friction that exists only because the plugin doesn't know a container is already protecting the host. |
| 2 | **dev-sandbox is invisible** | Not a plugin, not versioned, not in the marketplace. Install is a manual symlink; update is `git pull` + rebuild. ccflow and muxwatch never mention it (muxwatch integration exists only as an undocumented-in-ccflow mount in `claude-sand`). |
| 3 | **muxwatch is two installs and a manual daemon** | The plugin ships only hooks; the binary comes separately via `go install`/releases, and the user must start the daemon themselves. Marketplace install alone produces a silently non-functional plugin. |
| 4 | **muxwatch's name undersells it** | The core (daemon, IPC, status model, waybar/noctalia/dms frontends) is display-agnostic and already watches two agents (Claude Code + Codex). tmux is one adapter behind the `tmux.Client` interface — but the name, docs, and plugin description are all tmux-branded. |
| 5 | **No shared story for human-in-the-loop** | ccflow has decision gates (plan approval, `AskUserQuestion`), muxwatch has `NeedInput` signaling, dev-sandbox removes prompts — but nothing documents that these three things are the same feature seen from three sides. |
| 6 | **Autopilot can stall** | ccflow phases 2–9 run unattended, but nothing guarantees completion — a stopped turn mid-phase just stops. Claude Code now has `/goal` (run until a condition holds) and `/loop` (recurring runs), which fit this exactly. |

## 2. Target architecture

Three layers, one marketplace, one update command.

```
┌────────────────────────────────────────────────────────┐
│  attention layer   (renamed muxwatch)                  │
│  hooks → daemon → tmux · waybar · noctalia · dms       │
│  "Claude needs YOU" → NeedInput on every surface       │
├────────────────────────────────────────────────────────┤
│  workflow layer    (ccflow)                            │
│  human gates: refine · design · plan approval · AUQ    │
│  autopilot:  /goal-driven phases 2–9 → PR → CI green   │
│  babysit:    /loop → address-review until merged       │
├────────────────────────────────────────────────────────┤
│  isolation layer   (dev-sandbox)                       │
│  Docker/Podman + --dangerously-skip-permissions        │
│  the ONLY security boundary; no prompt friction inside │
└────────────────────────────────────────────────────────┘
```

### 2.1 Isolation layer — Docker with full permissions ✅ (this branch)

`claude-sand` now launches `claude --dangerously-skip-permissions` instead of an
`--allowedTools` list. The flag is container-safe by design (rejected as root; we run
as `dev`/1000). Only `~/Repos` is mounted; the host stays clean.

Follow-up (ticket 2): package dev-sandbox as a plugin in the marketplace so it is
versioned and updated like everything else — plugins can ship executables, so the
launcher, Dockerfile, and a `/sandbox:setup` skill (symlinks the launcher, builds the
image) travel with `claude plugin install` / `claude plugin update`.

### 2.2 Workflow layer — ccflow becomes container-native

- **`/ccflow:configure` detects the sandbox** (e.g. `/.dockerenv` or a `CLAUDE_SAND=1`
  env set by the launcher) and generates a container profile: no bubblewrap sandbox
  config, no Bash allowlists, no permission auto-fix phases, HTTPS git remotes
  assumed. Host profile (current behavior) remains for people not using the sandbox —
  but the sandbox becomes the documented default.
- **Keep every human gate exactly where it is**: refine/design interactivity, plan
  approval as the hard stop, `AskUserQuestion` only from the main agent. Nothing about
  full permissions changes the *decision* model — it only removes *mechanical* prompts.
- **`/goal` powers the autopilot**: on plan approval, implement sets a goal such as
  "all plan phases complete, PR open, CI green" so a mid-phase stop resumes instead of
  silently ending (requires Claude Code ≥ 2.1.139).
- **`/loop` powers babysitting**: a `ccflow:babysit <pr>` skill loops address-review +
  CI checks every N minutes until the PR merges — human only re-enters when a review
  comment is ambiguous (AskUserQuestion) or the watcher flags NeedInput.

### 2.3 Attention layer — rename muxwatch, demote tmux to an adapter

The tool watches *coding agents*, not tmux. Proposal:

- **Rename** (decision needed — see §4). Recommended: **`agentwatch`**.
- Restructure so tmux is one frontend among equals: `internal/frontend/tmux` next to
  waybar/noctalia/dms. The `tmux.Client` interface already makes this a move, not a
  rewrite. `notify` drops the hard `$TMUX_PANE` exit gate (key by session id; the tmux
  frontend just skips sessions with no pane).
- **Close the install gap**: the plugin's SessionStart hook bootstraps the binary
  (downloads the goreleaser artifact matching the plugin version into the plugin's
  `bin/` if missing) and starts the daemon if it isn't running. One
  `claude plugin install` → fully working.
- dev-sandbox keeps mounting the events socket, so sessions inside the container light
  up the same host status surfaces.

## 3. One install, one update

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install ccflow agentwatch sandbox
# later:
claude plugin update --all
```

Everything versions independently (existing per-plugin version-bump CI extends to the
sandbox plugin), but installs and updates through one mechanism.

## 4. Decisions (resolved 2026-07-08)

1. **muxwatch renames to `agentwatch`** — says what it does; agent-agnostic like the
   code; tmux becomes one frontend among waybar/noctalia/dms.
2. **dev-sandbox becomes a plugin** (`sandbox`) — versioned, marketplace-installed,
   updated via `claude plugin update` like the others.
3. **goal AND loop** — `/goal` for the implement autopilot, `/loop` for PR babysitting.

## 5. Migration plan (1 ticket = 1 PR)

| # | Ticket | Scope |
|---|--------|-------|
| 1 | ✅ dev-sandbox: full permissions in container | this branch |
| 2 | sandbox plugin packaging | plugin manifest, `/sandbox:setup` skill, version-bump CI, marketplace entry |
| 3 | rename muxwatch → agentwatch | Go module path, binaries, plugin/marketplace names, CI workflows, goreleaser, docs; keep `muxwatch/v*` tags frozen, start `agentwatch/v*` |
| 4 | agentwatch: binary bootstrap + daemon autostart | SessionStart hook downloads release binary + starts daemon; remove manual install steps |
| 5 | agentwatch: tmux as one frontend | `internal/frontend/tmux`, drop `$TMUX_PANE` gate in notify |
| 6 | ccflow: container profile in configure | sandbox detection, profile generation, docs |
| 7 | ccflow: goal-driven autopilot | set/clear goal around phases 2–9 |
| 8 | ccflow: babysit skill | `/loop`-based PR babysitting via address-review |
| 9 | docs: one-package README | root README rewrite around the three layers + single install path |
