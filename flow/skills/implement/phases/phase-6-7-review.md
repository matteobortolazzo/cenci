# Phase 6 + 7: Security, Code, And Silent-Failure Review

Read this file only when Phase 6 + 7 starts.

These quality gates are mandatory. `agentflow.reviewConcurrency` controls only whether reviewers run in parallel or sequentially.

Any point below that stops for the user — an unclear security fix, a code-review human decision, an issue that persists after 2 fix attempts — is an error gate: clear the Goal Autopilot first (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`), then stop and ask. Otherwise the goal restarts the turn instead of waiting for the decision.

## Shared Context

Source `ticketId`/`slug` from the plan front matter (`hasPlanFile` is always true here) before writing or reading any temp file below.

Gather context once. The worktree must be the CWD first — run a standalone `cd <worktree-path>` before these commands so the `git diff` calls resolve against the worktree and stay auto-approved:

```bash
git diff > /tmp/claude/agentflow-<ticket-id-or-slug>-diff.patch
git diff --name-only > /tmp/claude/agentflow-<ticket-id-or-slug>-files.txt
git diff --stat > /tmp/claude/agentflow-<ticket-id-or-slug>-stat.txt
```

For small diffs and `agentflow.diffContextMode` not set to `"file"`, inline the diff. For large diffs or file mode, pass reviewers the patch path, changed file list, stat, ticket requirements, and implementation plan. Tell reviewers to read only relevant hunks/files.

Source ticket requirements and plan from the plan file when `hasPlanFile` is true.

## Review Path Classification

Classify this diff into one of three review paths before launching any reviewer. Before evaluating any rule, delete/overwrite `/tmp/claude/agentflow-<ticket-id-or-slug>-review-path.txt` (e.g. `rm -f`) so a stale value from an earlier run of this ticket can never be trusted if this classification is interrupted partway (e.g. context compaction mid-phase) — an absent file safely falls back to the "full trio" default documented in Phase 9. Evaluate the rules below **in order — first match wins**. Read `/tmp/claude/agentflow-<ticket-id-or-slug>-files.txt` (changed file list) and `/tmp/claude/agentflow-<ticket-id-or-slug>-stat.txt` (total changed lines) from the Shared Context step above.

**Danger check** (used by Rules 2 and 3 below): a diff matches the danger check if **either** of these hold, case-insensitive:

- Any changed path contains one of: `auth`, `security`, `secret`, `credential`, `token`, `password`, `payment`, `.env`, `.github/workflows/**`.
- The diff content itself (added/removed lines in `/tmp/claude/agentflow-<ticket-id-or-slug>-diff.patch`) contains one of the same keywords — this catches an innocuously-named file whose diff content adds something like `"apiSecret": "..."`.

0. **Toggle off**: if `.claude/config.json` sets `agentflow.liteReviewEnabled` to `false` → path = **full**. Skip the remaining rules entirely.
1. **Force-full override**: if any changed file matches `.claude/**`, `**/skills/**`, `**/agents/**`, or `CLAUDE.md` (root or any nested `**/CLAUDE.md`) → path = **full**, regardless of size or file type. This check runs before the docs-only check below, so a `CLAUDE.md`-only or `skills/**`-only change never qualifies as docs-only, no matter how small.
2. **Docs-only**: if every changed file is a prose file — `README.md`, `docs/**`, `LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, or any other `**/*.md` **outside** the force-full set above — **and** the diff does not match the danger check above → path = **lite-docs**. No reviewers run at all. If every file is prose but the danger check matches (e.g. `docs/security-notes.md`, `SECURITY.md`, or a danger keyword in the diff content), do not take lite-docs — fall through to Rule 3/4 instead; a docs-only diff can still leak a credential or contain security-sensitive prose, so skipping all review here is the highest-risk gap.
3. **Small + safe**: if **all** of the following hold → path = **lite-small**:
   - Total changed lines (insertions + deletions, from `agentflow-<ticket-id-or-slug>-stat.txt`) < 20.
   - Every changed file is on the config/data allowlist: `*.json`, `*.yaml`/`*.yml` (excluding `.github/workflows/**`), `*.toml`, `*.txt`, `*.csv`, or another plain data/fixture file. Source/logic file extensions (`.ts`, `.tsx`, `.js`, `.jsx`, `.go`, `.py`, `.rb`, `.cs`, `.sh`, `.java`, `.rs`, or any other language/logic extension) are **never** on this allowlist, no matter how small the diff — a one-line source fix is never lite-small.
   - The diff does not match the danger check above.
4. **Otherwise** (ambiguous, over threshold, or any file off the allowlist) → path = **full**. Err toward full when uncertain. Ambiguity includes: `/tmp/claude/agentflow-<ticket-id-or-slug>-files.txt` or `/tmp/claude/agentflow-<ticket-id-or-slug>-stat.txt` is missing, empty, or the changed-line count cannot be confidently parsed as a number — resolve any of these to **full** before assuming 0 changed lines.

Write the chosen path string — `full`, `lite-docs`, or `lite-small` — to `/tmp/claude/agentflow-<ticket-id-or-slug>-review-path.txt`.

## Execution

Branch on the classification result written above:

- **`lite-docs`**: launch no reviewers. Docs-only changes skip Phase 6 + 7 review entirely; proceed to the next phase.
- **`lite-small`**: launch `code-reviewer` only, with the same diff/path, changed files, ticket requirements, and implementation plan it would receive on the full path. The existing Code Review Actions below apply unchanged — this is a narrower reviewer set, not a cheaper model tier. `agentflow.reviewConcurrency` has no effect with a single reviewer.
- **`full`**: launch all three reviewers:

  - `security-reviewer`: diff/path plus changed files.
  - `code-reviewer`: diff/path, changed files, ticket requirements, implementation plan.
  - `silent-failure-hunter`: diff/path plus changed files.

  Default: launch all three in one message. If `agentflow.reviewConcurrency` is `"sequential"`, run the same reviewers one at a time in this order: security, code, silent-failure. Do not skip a reviewer.

## Security Review Actions

The security reviewer checks OWASP, auth/authz, validation, injection, sensitive data, logging, and error exposure.

- Critical/High: fix immediately, rerun tests, rerun security review.
- Medium/Low: note in PR description unless trivial to fix.
- Unclear fix: ask the user via `AskUserQuestion`.

Security-critical findings take priority over code quality findings.

## Code Review Actions

The code reviewer uses confidence scoring and reports only findings >= 50.

- Must Fix >= 90: fix all, rerun tests.
- Should Fix 75-89: fix if straightforward; otherwise note in PR description.
- Nitpicks 50-74: ignore unless trivial.
- Human decision: stop and ask the user via `AskUserQuestion`.

If `REQUEST_CHANGES`, delegate fixes to implementer, rerun tests, and rerun code review. If the same issue persists after 2 fix attempts, escalate to the user.

After any fix-and-rerun cycle that changes the diff, re-run the Shared Context git commands and re-evaluate the Review Path Classification against the *current* diff before re-invoking any reviewer. If the recomputed path is now **full** (e.g. the fix touched a force-full path, added a source file, or pushed changed lines over threshold) but the original run only launched `code-reviewer` (`lite-small`) or no reviewers (`lite-docs`), launch `security-reviewer` and `silent-failure-hunter` for the first time before proceeding — never leave a widened diff under-reviewed just because the original classification was narrower.

## Silent Failure Actions

The silent-failure hunter checks for swallowed errors, empty catch blocks, silent fallbacks, and missing error propagation.

- Critical in auth/payment/data-loss paths: fix immediately.
- Warning in non-critical paths: fix if straightforward; otherwise note in PR description.
- Info with intentional suppression and comments: no action.
