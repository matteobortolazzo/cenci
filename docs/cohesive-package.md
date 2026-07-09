# Proposal: one cohesive package

Status: accepted · 2026-07-08 (decisions in §4) · updated 2026-07-09: orchestration
layer added (§2.4, decisions 5–8, tickets 11–16); Codex first-class support audit
added (§6, tickets 17–19)

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

- **A launcher instead of personal scripts** — `agentwatch run <workflow> [ticket]
  [--agent claude|codex]` (ticket 12, ✅ delivered): built-in Go templates cover claude
  refine/design/implement zero-config, with an optional
  `~/.config/agentwatch/config.json` overriding them and adding agents/workflows. It
  owns the `<number>-<slug>` naming (setting `automatic-rename off` so the daemon
  preserves the join key), chooses sandbox vs host (`claude`→`claude-sand`), and refuses
  grouped sessions. A board action shrinks to
  `command: "agentwatch run implement {number}"`. Which agent implements is a
  per-dispatch choice — Claude Code or Codex today depending on the work; opencode later
  is pure config, no code. This is also the
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
| 8 | ✅ ccflow: babysit skill | [#31](https://github.com/matteobortolazzo/claude-tools/issues/31) | `/loop`-based PR babysitting via address-review |
| 9 | docs: one-package README | [#32](https://github.com/matteobortolazzo/claude-tools/issues/32) | root README rewrite around the layers + single install path |
| 10 | codex: first-class support audit | [#33](https://github.com/matteobortolazzo/claude-tools/issues/33) | define proper Codex support per layer → follow-up tickets; includes Codex launcher templates for ticket 12 |
| 11 | agentwatch: public status client package | [#39](https://github.com/matteobortolazzo/claude-tools/issues/39) | export read-side snapshot types + subscribe client (`pkg/watch`); additive-only JSON contract |
| 12 | ✅ agentwatch: `run` launcher subcommand | [#40](https://github.com/matteobortolazzo/claude-tools/issues/40) | agent-agnostic dispatch (claude/codex), session naming, sandbox choice, `/goal` wiring; replaces personal scripts |
| 13 | ccflow: `In Review` board state | [#41](https://github.com/matteobortolazzo/claude-tools/issues/41) | phase 9 sets `In Review` at PR-open; babysit sets `Implemented` on merge |
| 14 | docs: board-orchestration recipe | [#42](https://github.com/matteobortolazzo/claude-tools/issues/42) | `docs/orchestration.md`: state machine, example board config, join-key convention, multi-agent notes |
| 15 | lazyboards: agent status on cards | [lazyboards#255](https://github.com/matteobortolazzo/lazyboards/issues/255) | subscribe via ticket 11's client, badge cards, NeedInput loudest, status-bar summary |
| 16 | lazyboards: jump to card's session | [lazyboards#256](https://github.com/matteobortolazzo/lazyboards/issues/256) | keybinding to focus the card's tmux window |
| 17 | agentwatch: self-contained Codex bootstrap parity | TBD | Codex SessionStart hook downloads the version-matched binary with SHA-256 verification + autostarts the daemon (like #27); symlinks the binary onto `$PATH`; redirects the "#33" pointers in both READMEs. From §6 audit |
| 18 | sandbox: launch Codex in the container | TBD | `claude-sand --agent claude\|codex`: mount host `codex`, stage `~/.codex/auth.json`/`OPENAI_API_KEY`, swap in Codex's full-permission flag; Dockerfile unchanged. Distinct from #40's dispatch launcher. From §6 audit |
| 19 | ccflow: documented Codex workflow equivalent | TBD | `AGENTS.md` template carrying TDD/review/worktree/PR conventions as prose + the Codex command template #40 uses for `--agent codex`; NOT a port of the skill/subagent pipeline. Feeds decision 7. From §6 audit |

## 6. Codex first-class support (audit)

Decision 4 made Codex a first-class target; #33 scoped what that means, layer by
layer. This section is the audit's output. It is deliberately **docs-only** — its
deliverable is this writeup plus the three drafted follow-up tickets (17–19 above).

**Distribution is already solved and needs no port.** Verified against `openai/codex`
`rust-v0.143.0`: Codex deliberately consumes Claude-format plugin infrastructure. It
reads `.claude-plugin/marketplace.json`, accepts `.claude-plugin/plugin.json` as a
manifest fallback, loads `hooks/hooks.json`, and sets `CLAUDE_PLUGIN_ROOT`. So "proper
support" for distribution reduces to **one marketplace, not two**: keep plugin layouts
Codex-clean (its `hooks.json` parser is `deny_unknown_fields` — no stray keys), and
document the `/hooks` trust re-approval step. That `/hooks` note folds into the
one-package README (#32); hook-spec correctness is tracked in #36. No new ticket.

### 6.1 Layer 1 — agentwatch (closest today)

A Codex plugin already exists at `agentwatch/plugin/codex/` (`hooks.json`,
`.codex-plugin/plugin.json`, `README.md`). It wires 6 events — SessionStart,
UserPromptSubmit, PermissionRequest, PreToolUse, PostToolUse, Stop — all firing
`agentwatch notify -agent codex`.

**Already covered (no ticket):**

- **Session cleanup.** Codex has no `SessionEnd` upstream, but the daemon's two sweeps
  cover it. The tmux pane-based sweep (`internal/frontend/tmux/frontend.go` Phase 3,
  `agentCommandMatches`) restores the window when Codex exits to the shell; the paneless
  TTL sweep (`daemon.go` `ttlSweep`, default 2h) expires paneless sessions. Covered by
  `TestDaemon_CodexLifecycleWithoutSessionEndRestoresAfterExit`. The trade-off is ~30s
  latency vs instant — expected behavior, not a gap.

**Routed to #36 (not re-ticketed):** hook-coverage gaps — missing `PostToolUseFailure`
(interrupt/ESC detection, without which an interrupted session stays stuck at Running)
and confirming `Notification` vs `PermissionRequest` equivalence. These are hook-spec
correctness, which #36 already owns.

**Real gap → Ticket 17.** Claude's SessionStart hook runs `plugin/hooks/bootstrap.sh`
(downloads the versioned binary with SHA-256 verification, starts the daemon). The Codex
`SessionStart` hook only calls a bare `agentwatch` and relies on the Claude plugin having
already bootstrapped — so a Codex-only install is silently non-functional. Because Codex
invokes `agentwatch` on `$PATH` (not `${CLAUDE_PLUGIN_ROOT}/bin`), parity also requires
symlinking/copying the binary onto `$PATH`. Both `agentwatch/README.md:80` and
`plugin/codex/README.md` currently carry a dangling "tracked in #33" pointer for this;
Ticket 17 redirects them to its own number. Distinct from #36 (hook-spec) and #27
(Claude-only bootstrap).

### 6.2 Layer 2 — dev-sandbox

The `claude-sand` launcher is hardcoded to `claude` at every invocation
(`CLAUDE_CMD_ARGS=(--dangerously-skip-permissions)`, literal `claude` in both the
fresh-start and attach paths). Auth is staged by ro-mounting
`~/.claude/.credentials.json` + `gh` `hosts.yml` and copying them into place in
`entrypoint.sh`; the `claude` binary is bind-mounted from the host
(`readlink -f $(which claude)`), not baked into the image. There is **no `codex`
anywhere**.

The Dockerfile needs no change — Codex mounts the same way. Adding Codex is
launcher-only work → **Ticket 18**: add `--agent claude|codex` (default claude), mount
host `codex` via `readlink -f $(which codex)`, stage Codex auth (`~/.codex/auth.json`
for ChatGPT sign-in and/or forward `OPENAI_API_KEY`) using the same ro-mount-then-copy
pattern, and swap the Claude-only `--dangerously-skip-permissions` for Codex's
full-permission flag. This is the *interactive* launcher, distinct from #40's
`agentwatch run --agent` dispatch launcher.

### 6.3 Layer 3 — ccflow (workflow)

ccflow has zero Codex mentions and is architecturally Claude-Code-only at every layer:
the plugin/skill system, the `Task` tool for subagents, `AskUserQuestion` gates, the
hook lifecycle (`PreToolUse`/`PreCompact`/`SessionStart`/`Stop`), `CLAUDE_PLUGIN_ROOT`,
and `.claude/settings.json`. Only the workflow *logic* — TDD red/green/refactor, the
review conventions, the worktree + `gh` PR flow — is portable, and only as prose.

**Decision (owner): a documented equivalent, not a port.** Porting the
skill/subagent/AskUserQuestion pipeline is out of scope. Instead ship an `AGENTS.md`-based
Codex recipe carrying the conventions, which the #40 dispatch launcher invokes for
`--agent codex`. This aligns with decision 7 ("Codex templates defined in #33"). →
**Ticket 19**.

### 6.4 Summary

| Layer | State | Outcome |
|-------|-------|---------|
| Distribution | Solved (Codex consumes Claude-format infra) | One marketplace; `/hooks` note → #32 |
| agentwatch — cleanup | Covered by two daemon sweeps | Document; no ticket |
| agentwatch — hooks | Gaps (`PostToolUseFailure`, `Notification`) | Routed to #36 |
| agentwatch — bootstrap | Gap (Codex install non-functional standalone) | **Ticket 17** |
| dev-sandbox | Gap (launcher is `claude`-only) | **Ticket 18** |
| ccflow | Claude-Code-coupled; only logic is portable | **Ticket 19** (documented equivalent) |
