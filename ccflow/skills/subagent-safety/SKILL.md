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

## Container profile (claude-sand)

When ccflow runs under the **container profile** (`.claude/config.json` has `profile: "container"`, set when configure detects the `claude-sand` container), Claude Code runs with `--dangerously-skip-permissions`. This changes two things about the rules above, but leaves the deadlock rules intact:

- **Permission prompts cannot occur** — skip-permissions suppresses them. The `Bash(gh *)` allow-rule check that normally gates read-only `gh` in subagents is therefore **moot**.
- **`gh auth status` is still required before delegation.** Auth is orthogonal to the permission model: an unauthenticated `gh` call in the context-gatherer subagent still deadlocks silently. The implement pre-flight keeps this check on the container profile.
- **`AskUserQuestion` still deadlocks in subagents** — the container changes nothing here. Keep it main-agent-only.
- **Mutating `gh` commands remain main-agent-only** — they require auth tokens regardless of the permission model.

## Subagent delegation and 1M-context sessions

Subagents don't inherit a 1M-context session's extra-usage entitlement, so when the
main session runs a 1M model, `Task` delegation can fail with "Usage credits required
for 1M context" — even with a `model: sonnet` override (Claude Code bug #51060 / #57249).
ccflow's fix is to pin every subagent to a 200K model via `CLAUDE_CODE_SUBAGENT_MODEL`.

You cannot reliably determine your own session model or context size from inside the
session — so don't try, and never announce them. The only thing to verify before the
first delegation is that the pin is set. Run once:

```bash
echo "${CLAUDE_CODE_SUBAGENT_MODEL:-unset}"
```

- **Returns a model id** (e.g. `claude-sonnet-4-6`) → the pin is set; delegation is safe.
  Proceed silently — do not comment on your model, the pin, or "1M context".
- **Returns `unset`** → proceed, but if a subagent then fails with "Usage credits required
  for 1M context", stop and tell the user:
  > "ccflow delegation is gated: this session is on a 1M-context model and subagents aren't
  > pinned to 200K (Claude Code bug #51060). Run `/ccflow:configure` (sets
  > `CLAUDE_CODE_SUBAGENT_MODEL=claude-sonnet-4-6`) and restart, or run `/model sonnet` for
  > this session, then re-invoke."
