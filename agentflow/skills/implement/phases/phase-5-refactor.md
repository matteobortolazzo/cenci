# Phase 5: Refactor

Read this file only when Phase 5 starts. Skip this separate phase if Phase 3 is running approved compact implementation mode.

Delegate to the `implementer` agent for focused cleanup of touched code only.

## Process

Pass:

- Worktree path. Tell the agent: enter it with a standalone `cd <worktree-path>` as the first Bash call (CWD persists for later calls) — do **not** prefix every command with `cd <path> &&`. See the `shell-rules` skill for command patterns.
- Changed file list.
- LSP diagnostic reminder if configured.

Review changed code for:

- Dead code or unnecessary abstractions.
- Duplicated logic; consolidate only when used 3+ times or clearly established locally.
- Unclear names.
- Complex conditionals that can be simplified.
- Overly clever code.

Run the full test suite after refactoring. Behavior must not change.

## Error Recovery

If tests fail, identify the specific refactoring step that broke behavior, revert that step only, and try a simpler cleanup or skip it.
