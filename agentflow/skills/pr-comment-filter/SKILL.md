---
name: pr-comment-filter
description: Decide which pull-request review comments are actionable. Use when addressing review feedback, monitoring a PR, or filtering already-handled comments.
user-invocable: false
---

## Actionable Comment Filter

This is the single source of truth for "is this PR comment actionable?". `address-review` (Phase 1D) and `babysit` (step 5) both apply exactly this filter — babysit's watermark only works if the two stay identical, so never restate the lists inline in a calling skill; reference this skill instead.

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
