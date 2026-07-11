# Phase 2: Worktree Setup

Read this file only when Phase 2 starts.

## Gate Check

Phase 2 only runs when `hasPlanFile` was set to true during mode detection — that is, the skill was **invoked** with a `.plans/<filename>.md` argument. A plan file written by Phase 1 in the current session does NOT satisfy this gate: new-plan sessions end at Phase 1, and implementation resumes in a fresh session.

Verify:

1. The invocation used a `.plans/<filename>.md` path (plan file mode from mode detection).
2. The plan file exists and was read.

If either check fails, stop and tell the user to run `/agentflow:implement .plans/<filename>` in a fresh session.

## Arm Goal Autopilot

The Gate Check passing means this session is committed to running phases 2–9. Arm the completion goal now, following the **Goal Autopilot (plan-file mode)** section of `SKILL.md`:

1. If `.claude/config.json` sets `agentflow.goalAutopilot: false`, skip — proceed to Create Worktree.
2. Otherwise run `claude --version` and version-gate on ≥ 2.1.139. If older, unparseable, or the command is unavailable, print the one-line unavailable notice and proceed without a goal.
3. If available, invoke `/goal` (via the `SlashCommand` tool) with the plan-file-referencing condition from `SKILL.md`, substituting the actual `.plans/<filename>` for this run. If `SlashCommand` is unavailable or errors, proceed without a goal.

This runs once, here — do not re-arm in later phases. Phase 9 clears the goal after the PR is created; any error gate that stops for user input clears it first (see `SKILL.md`).

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
