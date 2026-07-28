---
name: pr-comment-filter
description: Decide which pull-request review comments are actionable. Use when addressing review feedback, monitoring a PR, or filtering already-handled comments.
user-invocable: false
---

## Actionable Comment Filter

This is the single source of truth for "is this PR comment actionable?". `address-review` (Phase 1D) applies this filter in-session, evaluating every comment against the Include/Exclude lists below directly — never restate the lists inline in a calling skill; reference this skill instead.

`babysit` is a thin adapter over the client-neutral `cenci babysit` Go supervisor (`watch/internal/babysit/`), which forbids in-session polling. The supervisor itself applies only the **mechanical** subset of the Exclude list below — bot logins, resolved threads, outdated comments — nothing semantic. It cannot judge whether a comment is "purely informational" or a genuine change request; that semantic judgment is applied by the model session the supervisor dispatches when new PR activity is detected. This skill remains the single source of truth: if the Include/Exclude lists below ever change, edit this file first — the supervisor and every calling skill read from it, never the reverse.

**Include**:
- Unresolved comments/threads
- Comments requesting changes (not approvals or neutral comments with no actionable content)
- Inline code review comments with suggestions

**Exclude**:
- Bot-generated comments (author is a known bot: `github-actions[bot]`, `dependabot[bot]`, etc.)
- Already-resolved threads
- Comments that are purely informational with no action requested
- Outdated comments on code that no longer exists (GitHub `outdated` flag)

If no actionable comments remain after filtering, the calling skill reports that and proceeds accordingly (address-review stops; babysit treats it as a quiet tick).
