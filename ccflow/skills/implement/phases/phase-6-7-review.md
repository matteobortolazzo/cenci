# Phase 6 + 7: Security, Code, And Silent-Failure Review

Read this file only when Phase 6 + 7 starts.

These quality gates are mandatory. `ccflow.reviewConcurrency` controls only whether reviewers run in parallel or sequentially.

Any point below that stops for the user — an unclear security fix, a code-review human decision, an issue that persists after 2 fix attempts — is an error gate: clear the Goal Autopilot first (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`), then stop and ask. Otherwise the goal restarts the turn instead of waiting for the decision.

## Shared Context

Gather context once. The worktree must be the CWD first — run a standalone `cd <worktree-path>` before these commands so the `git diff` calls resolve against the worktree and stay auto-approved:

```bash
git diff > /tmp/claude/ccflow-diff.patch
git diff --name-only > /tmp/claude/ccflow-files.txt
git diff --stat > /tmp/claude/ccflow-stat.txt
```

For small diffs and `ccflow.diffContextMode` not set to `"file"`, inline the diff. For large diffs or file mode, pass reviewers the patch path, changed file list, stat, ticket requirements, and implementation plan. Tell reviewers to read only relevant hunks/files.

Source ticket requirements and plan from the plan file when `hasPlanFile` is true.

## Execution

Default: launch all three reviewers in one message:

- `security-reviewer`: diff/path plus changed files.
- `code-reviewer`: diff/path, changed files, ticket requirements, implementation plan.
- `silent-failure-hunter`: diff/path plus changed files.

If `ccflow.reviewConcurrency` is `"sequential"`, run the same reviewers one at a time in this order: security, code, silent-failure. Do not skip a reviewer.

## Security Review Actions

The security reviewer checks OWASP, auth/authz, validation, injection, sensitive data, logging, and error exposure.

- Critical/High: fix immediately, rerun tests, rerun security review.
- Medium/Low: note in PR description unless trivial to fix.
- Unclear fix: ask the user.

Security-critical findings take priority over code quality findings.

## Code Review Actions

The code reviewer uses confidence scoring and reports only findings >= 50.

- Must Fix >= 90: fix all, rerun tests.
- Should Fix 75-89: fix if straightforward; otherwise note in PR description.
- Nitpicks 50-74: ignore unless trivial.
- Human decision: stop and ask the user.

If `REQUEST_CHANGES`, delegate fixes to implementer, rerun tests, and rerun code review. If the same issue persists after 2 fix attempts, escalate to the user.

## Silent Failure Actions

The silent-failure hunter checks for swallowed errors, empty catch blocks, silent fallbacks, and missing error propagation.

- Critical in auth/payment/data-loss paths: fix immediately.
- Warning in non-critical paths: fix if straightforward; otherwise note in PR description.
- Info with intentional suppression and comments: no action.
