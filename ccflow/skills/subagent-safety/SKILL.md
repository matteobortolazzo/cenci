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

**Main-agent-only operations** (never delegate):
- `AskUserQuestion` — user interaction deadlocks in subagents. This also means reference skills that use `AskUserQuestion` (e.g. `attachments`) must only be invoked from the main agent
- `git push`, `git fetch`, `git pull` — require auth tokens
- `gh` commands — require auth tokens
- PR creation, ticket updates, comment replies — require auth tokens
- Any operation that may trigger a permission prompt

## 1M-context sessions block delegation

Before delegating, check your current session model ID (shown in your environment). If it contains `[1m]` (e.g. `claude-opus-4-8[1m]`), **stop** — do not delegate and do not silently run the pipeline inline.

The 1M flag is session-level: every subagent inherits it but not the session's extra-usage entitlement, so `Task` delegation fails with "Usage credits required for 1M context" — even with a `model: sonnet` override and even with usage credits enabled (Claude Code bug #51060 / #57249). ccflow requires 200K context.

Tell the user, substituting the real model ID:
> "Your session is on a 1M-context model (`<model-id>`), which blocks ccflow subagent delegation (Claude Code bug #51060). Run `/model opus` (or `/model sonnet`) to switch this session to 200K, then re-invoke. To make it permanent, run `/ccflow:configure` — it sets `CLAUDE_CODE_DISABLE_1M_CONTEXT` so new sessions start at 200K."

Then stop and wait for the user to switch.
