# Proposal: one cohesive package

Status: accepted · 2026-07-08 (decisions in §4) · updated 2026-07-09: orchestration
layer added (§2.4, decisions 5–8, tickets 11–16)

The repo today ships three good tools with three unrelated install stories and no shared
security model. This proposal turns them into one package with a single principle:

> **The board dispatches the work. The workflow owns the decisions.
> The container is the security boundary. The watcher routes your attention.**

Humans decide (refine, design, approve the plan, answer questions). An agent implements
on autopilot — with *all* permissions, because it is locked in a container, and with
`/goal` keeping it going until the work is actually done. The agent is Claude Code
today, Codex when the work fits it better, maybe opencode later — the dispatch and
attention layers must not care which (§4.4, §4.7).

## 1. Review: where the seams are

| # | Gap | Detail |
|---|-----|--------|
| 1 | **Two competing security models** | ccflow is built around Claude Code's *host* sandbox (bubblewrap, `allowedDomains`, Bash allowlists, deny rules) while dev-sandbox isolates via Docker with a tool allowlist. Running ccflow inside the sandbox means double isolation: SSH-push failures, `go` env workarounds, permission auto-fix prompts — friction that exists only because the plugin doesn't know a container is already protecting the host. |
| 2 | **dev-sandbox is invisible** | Not a plugin, not versioned, not in the marketplace. Install is a manual symlink; update is `git pull` + rebuild. ccflow and muxwatch never mention it (muxwatch integration exists only as an undocumented-in-ccflow mount in `claude-sand`). |
| 3 | **muxwatch is two installs and a manual daemon** | The plugin ships only hooks; the binary comes separately via `go install`/releases, and the user must start the daemon themselves. Marketplace install alone produces a silently non-functional plugin. |
| 4 | **muxwatch's name undersells it** | The core (daemon, IPC, status model, waybar/noctalia/dms frontends) is display-agnostic and already watches two agents (Claude Code + Codex). tmux is one adapter behind the `tmux.Client` interface — but the name, docs, and plugin description are all tmux-branded. |
| 5 | **No shared story for human-in-the-loop** | ccflow has decision gates (plan approval, `AskUserQuestion`), muxwatch has `NeedInput` signaling, dev-sandbox removes prompts — but nothing documents that these three things are the same feature seen from three sides. |
| 6 | **Autopilot can stall** | ccflow phases 2–9 run unattended, but nothing guarantees completion — a stopped turn mid-phase just stops. Claude Code now has `/goal` (run until a condition holds) and `/loop` (recurring runs), which fit this exactly. |
| 7 | **The orchestration glue is homemade and invisible** | The real entry point to the whole system is a [lazyboards](https://github.com/matteobortolazzo/lazyboards) kanban board whose column actions dispatch refine/design/implement — via unversioned personal scripts (`~/.config/lazyboards/scripts/*.sh`) that pin a stale model, run the host profile instead of the sandbox, and hand-roll the tmux window naming the watcher joins on. Same disease as gaps 2–3, one layer up: no layer of the package owns the board→workflow→watcher contract, and a future orchestrator *agent* would have to reverse-engineer dotfiles to dispatch work. |

## 2. Target architecture

Four layers. The bottom three live in this repo's marketplace; the top one is the
[lazyboards](https://github.com/matteobortolazzo/lazyboards) board in its own repo,
consuming contracts this repo exports (§2.4).

```
┌────────────────────────────────────────────────────────┐
│  orchestration layer   (lazyboards — separate repo)    │
│  board = state machine: columns ↔ ticket labels        │
│  keypress (later: agent policy) → agentwatch run       │
│  cards show live status; NeedInput → jump to session   │
├────────────────────────────────────────────────────────┤
│  attention layer   (renamed muxwatch)                  │
│  hooks → daemon → tmux · waybar · noctalia · dms       │
│  "the agent needs YOU" → NeedInput on every surface    │
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

### 2.4 Orchestration layer — the board is the cockpit

The loop already runs in production, held together by hand: lazyboards columns map to
ticket labels, and ccflow *itself* drives the transitions — refine applies `Refined`,
design applies `Designed`, phase 9 applies `Implemented`, all through a `Working`
in-progress marker. Cards move across the board as agents work; column `cleanup` hooks
kill the tmux windows when they leave. The join key between board and watcher also
already exists: dispatch names tmux windows `<number>-<slug>`, which agentwatch reports
back as `window_name` in its status snapshots.

What's missing is ownership of that contract (gap 7). Three pieces close it:

- **A launcher instead of personal scripts** — `agentwatch run <workflow> <ticket>
  [--agent claude|codex]` (ticket 12): config-driven mapping of (agent, workflow) →
  command template, owns the `<number>-<slug>` naming, chooses sandbox vs host, wires
  `/goal`. A board action shrinks to `command: "agentwatch run implement {number}"`.
  Which agent implements is a per-dispatch choice — Claude Code or Codex today
  depending on the work; opencode later is pure config, no code. This is also the
  future *agent-orchestrator* interface: a `/loop`-driven policy ("dispatch implement
  for every card in Designed, cap concurrency at N") calls the same CLI a human
  keypress does — human gates stay inside the dispatched sessions (refine/design
  interactivity, plan approval, NeedInput), not in the dispatcher.
- **The board becomes a watcher frontend** — agentwatch exports its read-side client
  (ticket 11: `StateSnapshot`/`WindowState` + subscribe client as a public Go package),
  and lazyboards badges cards with live status: NeedInput loudest, summary in the
  status bar, a keybinding to jump to the card's session (lazyboards #255/#256).
  "Claude needs YOU" rendered on the exact ticket that needs you.
- **One missing board state** — `Implemented` is applied at PR-*open*, so "review
  looping" and "merged" are indistinguishable. Phase 9 moves to `In Review`; babysit
  applies `Implemented` on merge (ticket 13). Full machine:
  New → Refined → Designed → In Review → Implemented, with `Working` as the
  transient marker.

**lazyboards stays a separate repo — deliberately.** It is a general-purpose kanban
TUI with its own audience and install path, and its README correctly never mentions
any agent. Merging it here would couple a clean product to agent-tooling branding
that is itself slated for a rename (§4.4). The earlier mistake was not the separation
— it was that *neither* repo owned the integration. With the launcher, the exported
client package, and a documented recipe (ticket 14) living here, lazyboards becomes a
well-behaved consumer over versioned contracts.

## 3. One install, one update

```bash
claude plugin marketplace add matteobortolazzo/claude-tools
claude plugin install ccflow agentwatch sandbox
# later:
claude plugin update --all
```

Everything versions independently (existing per-plugin version-bump CI extends to the
sandbox plugin), but installs and updates through one mechanism.

The board is the one optional extra, installed from its own repo
(`go install github.com/matteobortolazzo/lazyboards@latest`) and wired up by the
orchestration recipe (ticket 14).

## 4. Decisions (resolved 2026-07-08)

1. **muxwatch renames to `agentwatch`** — says what it does; agent-agnostic like the
   code; tmux becomes one frontend among waybar/noctalia/dms.
2. **dev-sandbox becomes a plugin** (`sandbox`) — versioned, marketplace-installed,
   updated via `claude plugin update` like the others.
3. **goal AND loop** — `/goal` for the implement autopilot, `/loop` for PR babysitting.
4. **Codex is a first-class target** — the package should properly support OpenAI Codex
   alongside Claude Code (agentwatch already watches both). The repo itself will be
   renamed later (owner action) to drop the Claude-only branding; until then, avoid
   baking `claude-` into new identifiers. Scope definition tracked in #33.

Resolved 2026-07-09 (orchestration layer):

5. **lazyboards is the orchestration layer and stays a separate repo** — the
   integration contract (launcher, exported watcher client, recipe) lives *here*;
   lazyboards consumes it over versioned Go modules. Rationale in §2.4.
6. **The launcher is `agentwatch run` and is agent-agnostic** — per-dispatch
   `--agent claude|codex` with a config default, since which agent gets a ticket
   depends on the work; opencode support later is config, not code. It rides the
   agentwatch binary, so the #27 bootstrap distributes it for free.
7. **Per-agent workflow templates, shared everything else** — board, launcher,
   watcher, and sandbox are agent-neutral; only the (agent, workflow) → command
   mapping differs. Claude templates call ccflow skills; Codex templates are defined
   in #33; the state-machine labels are shared so mixed-agent boards just work.
8. **Board state machine gains `In Review`** — PR-open sets `In Review`, merge sets
   `Implemented` (via babysit), so the babysit loop has a visible home state and
   column cleanup can reap its window on merge.

## 5. Migration plan (1 ticket = 1 PR)

| # | Ticket | Issue | Scope |
|---|--------|-------|-------|
| 1 | ✅ dev-sandbox: full permissions in container | [#34](https://github.com/matteobortolazzo/claude-tools/pull/34) | — |
| 2 | ✅ sandbox plugin packaging | [#25](https://github.com/matteobortolazzo/claude-tools/issues/25) | plugin manifest, `/sandbox:setup` skill, version-bump CI, marketplace entry |
| 3 | ✅ rename muxwatch → agentwatch | [#26](https://github.com/matteobortolazzo/claude-tools/issues/26) | Go module path, binaries, plugin/marketplace names, CI workflows, goreleaser, docs; keep `muxwatch/v*` tags frozen, start `agentwatch/v*` |
| 4 | agentwatch: binary bootstrap + daemon autostart | [#27](https://github.com/matteobortolazzo/claude-tools/issues/27) | SessionStart hook downloads release binary + starts daemon; remove manual install steps |
| 5 | agentwatch: tmux as one frontend | [#28](https://github.com/matteobortolazzo/claude-tools/issues/28) | `internal/frontend/tmux`, drop `$TMUX_PANE` gate in notify |
| 6 | ccflow: container profile in configure | [#29](https://github.com/matteobortolazzo/claude-tools/issues/29) | sandbox detection, profile generation, docs |
| 7 | ccflow: goal-driven autopilot | [#30](https://github.com/matteobortolazzo/claude-tools/issues/30) | set/clear goal around phases 2–9 |
| 8 | ccflow: babysit skill | [#31](https://github.com/matteobortolazzo/claude-tools/issues/31) | `/loop`-based PR babysitting via address-review |
| 9 | docs: one-package README | [#32](https://github.com/matteobortolazzo/claude-tools/issues/32) | root README rewrite around the layers + single install path |
| 10 | codex: first-class support audit | [#33](https://github.com/matteobortolazzo/claude-tools/issues/33) | define proper Codex support per layer → follow-up tickets; includes Codex launcher templates for ticket 12 |
| 11 | agentwatch: public status client package | [#39](https://github.com/matteobortolazzo/claude-tools/issues/39) | export read-side snapshot types + subscribe client (`pkg/watch`); additive-only JSON contract |
| 12 | agentwatch: `run` launcher subcommand | [#40](https://github.com/matteobortolazzo/claude-tools/issues/40) | agent-agnostic dispatch (claude/codex), session naming, sandbox choice, `/goal` wiring; replaces personal scripts |
| 13 | ccflow: `In Review` board state | [#41](https://github.com/matteobortolazzo/claude-tools/issues/41) | phase 9 sets `In Review` at PR-open; babysit sets `Implemented` on merge |
| 14 | docs: board-orchestration recipe | [#42](https://github.com/matteobortolazzo/claude-tools/issues/42) | `docs/orchestration.md`: state machine, example board config, join-key convention, multi-agent notes |
| 15 | lazyboards: agent status on cards | [lazyboards#255](https://github.com/matteobortolazzo/lazyboards/issues/255) | subscribe via ticket 11's client, badge cards, NeedInput loudest, status-bar summary |
| 16 | lazyboards: jump to card's session | [lazyboards#256](https://github.com/matteobortolazzo/lazyboards/issues/256) | keybinding to focus the card's tmux window |
