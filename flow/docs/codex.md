# Codex support

cenci installs native plugin skills, hooks, agent adapters, and launch templates for
Codex CLI 0.144.1 or newer. Shared project configuration lives in
`.cenci/config.json`; shared guidance lives in `AGENTS.md`.

## Install

```bash
codex plugin marketplace add matteobortolazzo/cenci
codex plugin add cenci@cenci
```

Review changed plugin hooks with `/hooks`. cenci never changes hook trust automatically.

## Native workflows

Explicitly mention `$cenci:configure`, `$cenci:refine`, `$cenci:implement`,
`$cenci:review`, `$cenci:address-review`, `$cenci:refactor`, `$cenci:sync`,
`$cenci:garden`, `$cenci:babysit`, or optional `$cenci:design`.

Planning workflows enter `/plan`, gather material choices, and produce an approved plan
without mutations. A second normal-mode invocation persists plans, updates GitHub, and
executes. Implementation uses `.cenci/checkpoints/`, generated `.codex/agents/*.toml`,
and a native goal armed only after plan approval. Missing custom agents fall back to
built-in workers with the same bounded role prompt.

The launcher provides the same surface:

```bash
cenci run refine 42 --agent codex
cenci run implement 42 --agent codex
cenci babysit 123 --agent codex --interval 15m
```

Pencil design remains optional and requires its CLI/MCP dependencies.

## Attention behavior

Cenci `need-input` renders a red/dim foreground `!`. A red tmux background without `!`
is usually tmux's native bell alert from a BEL notification. Run `cenci doctor` to inspect
Codex notification configuration; choose `osc9` yourself if BEL overlays are unwanted.
