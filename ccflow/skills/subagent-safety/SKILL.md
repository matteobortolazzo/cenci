---
name: subagent-safety
description: Rules for what operations can and cannot be delegated to subagents
user-invocable: false
---

## Subagent Safety

Subagents (Task tool) cannot surface permission prompts, authentication errors, or user questions to the main conversation. They block silently, appearing to hang.

**Subagent-safe operations** (delegate freely):
- Code reading, analysis, and review
- File searching and pattern matching
- Context7 documentation lookups
- Local file writes within the worktree
- Running builds and tests
- **Read-only `gh` commands** (`gh issue view`, `gh issue list`) — only after the main agent has verified, in the current session, that `Bash(gh *)` is in `permissions.allow` and `gh auth status` succeeds (the implement pre-flight check does both). Without that verification, a `gh` call can trigger a permission prompt or auth error inside the subagent and hang silently

**Main-agent-only operations** (never delegate):
- `AskUserQuestion` — user interaction deadlocks in subagents. This also means reference skills that use `AskUserQuestion` (e.g. `attachments` Steps 2–4) must only be invoked from the main agent
- `git push`, `git fetch`, `git pull` — require auth tokens
- Mutating `gh` commands (`gh issue edit`, `gh issue comment`, `gh pr *`, label changes) — require auth tokens and may prompt
- PR creation, ticket updates, comment replies — require auth tokens
- Any operation that may trigger a permission prompt
