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

## 1M-context sessions and delegation

A 1M-context session (model ID ends in `[1m]`, e.g. `claude-opus-4-8[1m]`) can gate `Task` delegation: the `[1m]` flag is session-level and attaches to every subagent request, but subagents don't inherit the session's extra-usage entitlement — so delegation can fail with "Usage credits required for 1M context", even with a `model: sonnet` override (Claude Code bug #51060 / #57249).

ccflow's fix is to pin subagents to a 200K model with `CLAUDE_CODE_SUBAGENT_MODEL`, so the main session keeps 1M while reviews run at 200K. Before delegating from a `[1m]` session, verify the pin is set — run:

```bash
echo "${CLAUDE_CODE_SUBAGENT_MODEL:-unset}"
```

- **Set to a non-`[1m]` model** (e.g. `claude-sonnet-4-6`) → proceed; subagents run at 200K.
- **`unset`** → delegation may be gated. Tell the user (substituting the real model ID), then stop — do not silently run the pipeline inline:
  > "Your session is on a 1M-context model (`<model-id>`) and subagents aren't pinned to 200K, so ccflow delegation may be gated (Claude Code bug #51060). Run `/ccflow:configure` (sets `CLAUDE_CODE_SUBAGENT_MODEL=claude-sonnet-4-6`) and restart, or run `/model sonnet` for this session, then re-invoke."

If a subagent still fails with "Usage credits required for 1M context" **even with the pin set**, the pin didn't strip `[1m]`. Stop and tell the user to run `/model sonnet` for this session, then re-invoke.
