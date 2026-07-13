# Codex support

Agentflow gives Codex shared engineering conventions as native plugin skills and the
ticket-to-PR workflow as an `AGENTS.md` prose recipe. The interactive Claude Code
pipeline is deliberately not ported.

## Install

The marketplace catalog is shared, but Claude Code and Codex keep separate plugin
installations:

```bash
codex plugin marketplace add matteobortolazzo/agent-stack
codex plugin add agentflow@agent-stack
```

The agent-stack installer runs these commands automatically when it detects Codex.
Restart or begin a new Codex session after installation.

Verified with Codex CLI 0.144.3: Codex accepts this repository's existing marketplace,
uses `agentflow/.codex-plugin/plugin.json`, and discovers the bundled
`skills/*/SKILL.md` files under the `agentflow:` namespace. A Claude installation
under `~/.claude/plugins` is not shared with Codex. No `.agents/skills` copy or
symlink is required.

Agentflow deliberately keeps lifecycle hooks client-specific. The Codex manifest
points to `codex/hooks.json`, whose empty `hooks` map explicitly declares that the
Codex surface has no lifecycle handlers. This prevents Codex from falling back to
`hooks/hooks.json`, which contains Claude Code-only commands and output contracts.
Claude Code continues to load that original hook file unchanged, while both clients
continue to discover the shared skills.

## Portable skills

Codex can automatically apply the convention skills for attachments, frontend
classification, PR-comment filtering, shell commands, Angular, .NET, Go, delegation,
testing, and worktrees. The complete matrix and limitations are in the
[agentflow README](../README.md#skill-portability).

Skills whose descriptions start with `Claude Code-only` are present so one
marketplace can serve both clients, but Codex must not invoke them. They depend on
Claude pipeline mechanics such as `AskUserQuestion`, model/invocation frontmatter,
hooks, slash commands, or specialized workflow subagents.

## Ticket-to-PR recipe

The recipe in [`templates/agents-md-codex.md`](../templates/agents-md-codex.md) is a
complete `AGENTS.md` a Codex session can follow:

- TDD red, green, and touched-code refactor
- Security, code-quality, and silent-failure self-review
- Worktree discipline
- Rebase, conventional commit, push, and pull-request flow

Portable skills supply reusable conventions while the recipe owns sequencing and
acceptance gates. Codex performs the pipeline roles using its available agent
capabilities; it does not invoke `/agentflow:implement`.

## Dispatch

1. Put the `AGENTS.md` recipe at the target repository root.
2. Install agent-stack for Codex so AgentWatch hooks and portable skills are present.
3. Merge [`templates/agentwatch-codex-config.json`](../templates/agentwatch-codex-config.json)
   into `~/.config/agentwatch/config.json`.
4. Launch:

   ```bash
   agentwatch run implement <n> --agent codex
   ```

The Codex workflow template resolves to `codex exec` in a `<ticket>-<slug>` tmux
window and tells Codex to follow the repository's `AGENTS.md`.

Only the `implement` dispatch is provided. Interactive refinement, Pencil design,
Claude goal/loop automation, and Claude project configuration remain out of scope.
The architectural rationale is recorded in
[`docs/cohesive-package.md` section 6.3](../../docs/cohesive-package.md#63-layer-3--agentflow-workflow).
