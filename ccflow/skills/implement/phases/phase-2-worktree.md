# Phase 2: Worktree Setup

Read this file only when Phase 2 starts.

## Gate Check

Phase 2 only runs when `hasPlanFile` is true. If no plan file exists, stop; Phase 1 should have persisted a plan and ended the skill.

Verify:

1. The invocation used a `.plans/<filename>.md` path, or Phase 1 just wrote one.
2. The plan file exists and was read.

## Create Worktree

Verify at least one commit exists:

```bash
git rev-parse HEAD 2>/dev/null
```

If the repository has no commits, create an initial commit:

```bash
git add -A && git commit -m "chore: initial commit" --allow-empty
```

Create the worktree:

- Ticket mode: `git worktree add .worktrees/<ticket-id>-<description> -b feature/<ticket-id>-<description>`
- Ticketless mode: `git worktree add .worktrees/<auto-slug> -b feature/<auto-slug>`

All subsequent phases run inside the worktree. Use absolute paths rooted at `<worktree-path>` when delegating file edits.
