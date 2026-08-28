# Codex support

cenci installs native plugin skills, hooks, agent adapters, launch templates, and PR
babysitting for Codex CLI 0.144.1 or newer. The full gated ticket-to-PR workflow remains
in development until behavioral end-to-end acceptance passes. Shared configuration lives in
`.cenci/config.json`; shared guidance lives in `AGENTS.md`.

## Install

```bash
codex plugin marketplace add matteobortolazzo/cenci
codex plugin add cenci@cenci
```

Review changed plugin hooks with `/hooks`. cenci never changes hook trust automatically.

## Native workflow foundation

Explicitly mention `$cenci:configure`, `$cenci:refine`, `$cenci:implement`,
`$cenci:review`, `$cenci:address-review`, `$cenci:refactor`, `$cenci:sync`,
`$cenci:maintain`, or `$cenci:babysit`.

The staged procedures are under active development. Their intended contract is: planning workflows enter `/plan`, gather material choices, and produce an approved plan
without mutations. A second normal-mode invocation persists plans, updates GitHub, and
executes. Implementation uses `.cenci/checkpoints/`, generated `.codex/agents/*.toml`,
and a native goal armed only after plan approval. Missing custom agents fall back to
built-in workers with the same bounded role prompt.

The launcher exposes development entry points:

```bash
cenci run refine 42 --agent codex
cenci run implement 42 --agent codex
cenci babysit 123 --agent codex --interval 15m
```

**Authoring for Codex.** A skill reaches Codex through a `skills/<name>/codex.md`
companion — a separate procedure for a client with different primitives, not a
translation of `SKILL.md`. See
[`skill-authoring.md`](skill-authoring.md#client-surfaces) for the three client surfaces
a skill change must reconcile, and [`adapter-contract.md`](adapter-contract.md) for the
narrower eight-property behavioral-parity contract the implement pipeline's Claude Code
and Codex adapters both have to satisfy.

## Agent role templates

Role TOMLs live at `templates/codex/agent-roles/`, not `templates/codex/agents/`
(#1040). An unscoped directory literally named `agents` sitting inside this Codex
plugin's own root (`flow/`) is suspected of being auto-discovered by Codex's plugin
loader as a second source of the same role names, which would explain the
"duplicate agent role name ... declared in the same config layer" warning users saw
alongside "must define a description" on every startup — the flow plugin's own
source is the one place that carries both the installed copies (`.codex/agents/`)
and the template source side by side. This couldn't be confirmed against a live
Codex binary; the rename removes the collision regardless of the exact mechanism.

`install-agents.sh` never overwrites an existing `.codex/agents/*.toml` (they're
user-editable), so a repo configured before a template fix lands stays on the
broken shape indefinitely. `hooks/scripts/check-agent-role-drift.sh` is a
SessionStart advisory (mirroring `check-config-staleness.sh`) that flags this: it
runs `codex/validate-agent-roles.sh --plain` against the installed
`.codex/agents/`, and surfaces a report-only nudge when validation fails. It never
rewrites files. The same validator backs `/cenci:maintain`'s `agent-roles` check.

## Attention behavior

Cenci `need-input` renders a red/dim foreground `!`. A red tmux background without `!`
is usually tmux's native bell alert from a BEL notification. Run `cenci doctor` to inspect
Codex notification configuration; choose `osc9` yourself if BEL overlays are unwanted.
