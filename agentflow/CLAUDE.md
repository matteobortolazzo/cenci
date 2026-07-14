# Project: agentflow

Claude Code plugin — Markdown skills, JSON config, shell hooks.
GitHub Issues for tracking. GitHub for code and PRs.

## Critical Rules
- ALWAYS read relevant `docs/` files when working in their topic area (e.g., `docs/git-workflow.md` before commits/PRs).
- Test-first: integration tests that assert behavior, not implementation details.
- No secrets, credentials, or API keys in code.
- No PII or stack traces in user-facing error responses.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.
- Interactive (Claude-only) skills must route every user question/confirmation through `AskUserQuestion` and never say a bare "ask the user"; cross-tool-portable skills use abstract wording instead (e.g., "the client's available user-input mechanism").
- All shared temp files written by phases or agents (e.g., `/tmp/claude/agentflow-<ticket-id-or-slug>-diff.patch`) must be uniquely scoped by worktree path, run ID, or session UUID to prevent silent collisions when multiple concurrent `/agentflow:implement` jobs execute in the same monorepo. Fixed paths without scoping cause competing runs to overwrite each other's state, risking reviewers analyzing wrong diffs or broken context.
- When adding a mandatory-restart rule to a multi-step pipeline (e.g., "any re-entry must restart at Rebase"), ensure every downstream step that could fail has documented recovery or idempotency handling — don't assume the first step's handling covers all cases. A rebase rewrites commit SHAs (push then fails non-fast-forward), and re-creating an already-existing PR fails with "PR already exists" — each needs explicit fallback branches documented in the phase markdown (e.g., `--force-with-lease` retry for push, `gh pr view` recovery for PR creation). Missing recovery at downstream steps causes operational failures on Goal Autopilot resume.
- When a new skill section or automated path reuses an existing error-handling or safety rule by reference but changes what happens after that step (e.g., removes a human checkpoint, continues into further autonomous phases, or arms an unattended completion loop), re-evaluate and explicitly restate the error-handling for the new risk profile. Do not assume the rule remains safe as-is in the new context — the original rule may have worked in its original stopping point but be under-specified for the new path.

## Reference Docs
On-demand topic docs live at `docs/`:
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- `docs/skill-authoring.md` — writing skills that generate/regenerate files, especially with external-sourced values
- `docs/ticket-sizing.md` — how tickets are sized against the ~200k agent context budget and when to split

`.claude/rules/` is reserved for files explicitly `@`-imported by this CLAUDE.md (auto-loaded at session start). It is not used today.
