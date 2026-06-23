# Phase 8: Capture Lessons And Update Docs

Read this file only when Phase 8 starts.

Default action: skip. Only run a sub-step when its trigger fired.

All file edits must land inside `<worktree-path>`. Use absolute paths rooted at the worktree when reading/writing or delegating.

Before any sub-step writes or delegates, verify `<worktree-path>` (from Phase 2) is an **absolute** path containing a `/.worktrees/` segment. If it is relative or has no `/.worktrees/` segment, do not delegate — stop and report, since any edit would be stranded in the main worktree.

## Capture Lessons

Run `lessons-collector` only if at least one occurred:

- A build/test failure needed a non-obvious fix.
- The wrong API/pattern was used, then corrected.
- An assumption caused rework.
- A reviewer flagged something the implementer should have caught.

Do not run for smooth sessions, normal TDD red/green progression, or obvious findings already covered by existing rules.

Pass that exact verified `<worktree-path>` as `<project-root>`. The collector routes to existing `docs/<topic>.md`, `CLAUDE.md` Critical Rules for project-wide invariants, or a new topic doc only when multiple findings cluster. It never writes to `.claude/rules/lessons-learned.md`.

Lessons must be specific, actionable, non-duplicate, and worth keeping permanently.

## Update CLAUDE.md

Update only for new architectural patterns, integration rules, or project-wide conventions future work must follow.

Append under `## Critical Rules`; do not rewrite existing content.

## Update README.md

Update only for user-visible features, commands, API endpoints, configuration, setup, or prerequisites. Keep changes minimal and match existing style.
