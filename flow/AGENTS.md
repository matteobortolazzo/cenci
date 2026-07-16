# Project: flow

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
- All shared temp files written by phases or agents (e.g., `/tmp/claude/cenci-<ticket-id-or-slug>-diff.patch`) must be uniquely scoped by worktree path, run ID, or session UUID to prevent silent collisions when multiple concurrent `/cenci:implement` jobs execute in the same monorepo. Fixed paths without scoping cause competing runs to overwrite each other's state, risking reviewers analyzing wrong diffs or broken context.
- Any mandatory-restart rule added to a multi-step pipeline (e.g., "re-entry restarts at Rebase") must document recovery/idempotency for every downstream step that could itself fail, not just the first — see `docs/pipeline-safety.md`.
- When a new skill section or automated path reuses an existing safety rule by reference but changes what happens after (removes a checkpoint, continues autonomously, arms an unattended loop), re-evaluate and restate that rule for the new risk profile — don't assume it still applies as-is. See `docs/pipeline-safety.md`.
- **Bash tool CWD does not reliably persist across multiple calls within a single subagent session** — an initial standalone `cd <worktree-path>` call does NOT guarantee subsequent Bash calls inherit that CWD. For verification-critical commands (test runs, build commands), especially when comparing before/after state (red vs green), always use fully absolute paths or re-verify CWD before each command, rather than relying solely on an initial `cd`. A wrong-directory execution produces a highly plausible but false result (e.g., running stale tests from the main worktree while thinking you're testing the worktree's changes). See implement skill's phase-3-test-red.md and phase-4-implement-green.md Delegation Context sections.

## Reference Docs
CLI grammar, alias, env-var, and naming conventions: `<repo-root>/docs/cli-conventions.md`.
On-demand topic docs live at `docs/`:
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- `docs/skill-authoring.md` — writing skills that generate/regenerate files, especially with external-sourced values
- `docs/ticket-sizing.md` — how tickets are sized against the ~200k agent context budget and when to split
- `docs/pipeline-safety.md` — restart/recovery and risk-profile re-evaluation rules for multi-phase pipelines

`.claude/rules/` is reserved for files explicitly `@`-imported by this AGENTS.md (auto-loaded at session start). It is not used today.
