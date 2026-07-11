# Codex support

agentflow's implementation workflow is available to OpenAI Codex as a **documented
prose equivalent**, not a port. This is a deliberate design decision — see
[`docs/cohesive-package.md` §6.3](../../docs/cohesive-package.md#63-layer-3--agentflow-workflow)
for the coupling rationale.

## What Codex gets

The recipe in [`templates/agents-md-codex.md`](../templates/agents-md-codex.md) is a
complete `AGENTS.md` a solo Codex session can follow unaided:

- **TDD loop** — red (failing tests that assert behavior) → green (simplest
  passing implementation) → refactor (touched code only).
- **Self-review** — the three agentflow reviewer conventions (security,
  code-quality, silent-failure) collapsed into one checklist Codex runs on its
  own diff.
- **Worktree discipline** — always work in a git worktree; the main worktree is
  read-only.
- **PR flow** — rebase, conventional commit, push, `gh pr create` with a
  structured body; 1 ticket = 1 PR; never commit to `main`.

Codex reads `AGENTS.md` from the repo root, so the conventions travel as that
file. A single Codex agent performs every role — there are no agentflow skills or
subagents for it to call.

## What stays Claude-only, and why

agentflow is architecturally Claude-Code-only at every layer, and only the workflow
*logic* is portable — as prose. These layers have no Codex equivalent and are
**not** ported:

- The plugin/skill system and `CLAUDE_PLUGIN_ROOT`.
- `Task` subagents (planner, implementer, the three reviewers).
- `AskUserQuestion` interactive gates.
- The hook lifecycle (`PreToolUse` / `PreCompact` / `SessionStart` / `Stop`).
- `.claude/settings.json`.
- `/goal` autopilot, `.plans/` plan files, babysit label automation, and Pencil.

This is a **deliberate prose equivalent, not a half-finished port**. Porting the
skill/subagent/`AskUserQuestion` pipeline is explicitly out of scope; the coupling
is documented in
[`docs/cohesive-package.md` §6.3](../../docs/cohesive-package.md#63-layer-3--agentflow-workflow).

## How it wires

1. Drop [`templates/agents-md-codex.md`](../templates/agents-md-codex.md) at the
   repo root as `AGENTS.md`.
2. Merge [`templates/agentwatch-codex-config.json`](../templates/agentwatch-codex-config.json)
   into `~/.config/agentwatch/config.json`. It adds a `codex` agent with an
   `implement` workflow in the shape agentwatch's `run` launcher consumes
   (`agents.codex` → `command` / `model` / `workflows`, with `{ticket}` and
   `{model}` placeholders). The prompt points Codex at the `AGENTS.md` recipe —
   **not** at `/agentflow:implement`, which is a Claude-only skill.
3. Launch:

   ```bash
   agentwatch run implement <n> --agent codex
   ```

   This resolves to `codex exec 'Implement GitHub ticket #<n> …'` in a
   `<n>-<slug>` tmux window.

Only the `implement` workflow is provided. `refine` and `design` lean on Pencil
and `AskUserQuestion`, which are not portable to a single-agent Codex loop.
