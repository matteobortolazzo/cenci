# AGENTS.md — ccflow workflow for Codex

This is the ccflow implementation workflow expressed as prose for a solo Codex
session. There are **no ccflow skills or subagents to call** — Codex reads this
file and performs every role itself: planner, implementer, and the three
reviewers. Follow the steps in order.

## Golden rules

- **1 ticket = 1 PR.** One unit of work, one branch, one pull request.
- **Never commit to `main`.** All work happens on a feature branch.
- **Always work in a git worktree.** The main worktree is read-only.
- **Tests first.** Write failing tests that assert behavior before writing code.
- **Self-review before opening the PR.** Run the security / code-quality /
  silent-failure checklists on your own diff and fix what they surface.

## Workflow

### 1. Understand the ticket

- Read the ticket: `gh issue view <n>`.
- Restate the acceptance criteria in your own words.
- List the edge cases and error scenarios the change must handle.
- If the requirements are genuinely unclear, stop and ask before writing code —
  do not guess at scope.

### 2. Worktree

Create an isolated worktree and branch off `main`, and do all work inside it:

```bash
git worktree add .worktrees/<id>-<desc> -b feature/<id>-<desc> main
```

- Operate **inside** the worktree for the whole task.
- The main worktree stays on `main` and is read-only.
- `.worktrees/` must be gitignored — add it to `.gitignore` if it isn't already.

### 3. Red — tests first

Write tests that fail because the behavior doesn't exist yet.

- Assert **behavior**: status codes, response shapes, state transitions, visible
  UI states, error behavior.
- **Never** assert call counts, internal method names, hardcoded magic values, or
  values copied from the implementation. Tests encode requirements, not code.
- Prefer integration tests that exercise the real flow end-to-end. Write unit
  tests only for complex domain logic: calculations, state machines, validation
  rules, or parsing.
- Tests must be readable, deterministic, and independent.
- Run the tests and confirm they fail for the expected reason. If a new test
  passes before any implementation exists, investigate whether it actually
  covers new behavior.

### 4. Green — simplest passing implementation

- Follow the plan; write the **simplest correct** code that makes the tests pass.
- No premature abstraction, no dead code, no commented-out code, no TODOs without
  a ticket reference.
- Update `README`/docs if behavior, setup, configuration, or a user-visible
  contract changes.
- Run the **full build** and the **full test suite**. On failure, analyze the
  root cause, fix, and rerun. Cap this at ~3 attempts; if it still fails, stop
  and report the exact commands, errors, and your best hypothesis.

### 5. Refactor — touched code only

Clean up only the code you changed; behavior must not change.

- Remove dead code and unnecessary abstractions.
- Consolidate duplicated logic **only** when it appears 3+ times or is clearly
  established locally.
- Clarify unclear names; simplify complex conditionals; remove overly clever code.
- Rerun the full test suite. If a refactor breaks a test, revert that one step and
  try a simpler cleanup or skip it.

### 6. Self-review

You are the reviewer too. Run all three checklists over your diff
(`git diff origin/main...`). Fix what each requires before moving on.

**Security review.** Trace data flow from input to storage/output.

- Authorization is enforced at every access point.
- Input validation exists and is correct.
- No injection: SQL, XSS, or command injection.
- No secrets, PII, or tokens in logs or error responses.
- No stack traces leaked to users.
- Cover the OWASP Top 10 items relevant to the diff.
- **Actions**: Critical/High → fix and rerun tests; Medium/Low → note in the PR
  description unless trivial to fix. Security findings take priority over
  code-quality findings.

**Code-quality review.**

- Follows the project's conventions.
- Covers the ticket's acceptance criteria.
- No obvious bugs; edge cases handled; error handling complete.
- Performance is acceptable; names are clear.
- No dead or commented-out code; no TODOs without ticket references.
- No unused variables or type errors; documentation is adequate.
- **Severity tiers**: Must-Fix → fix all. Should-Fix → fix if straightforward,
  otherwise note in the PR. Nitpick → ignore unless trivial.

**Silent-failure review.** Scan every new or modified `catch` / `except` /
`rescue` / `.catch(` / error boundary.

- **Critical**: empty catch blocks; catch-and-ignore; swallowed errors in
  auth/payment/data paths; missing error propagation in async code; returning
  HTTP 200 when the operation actually failed.
- **Warning**: silent fallback to a default on error; log-only handling in
  production; returning `false`/`null` on error instead of throwing; no-op
  `.catch()`; an over-broad `try` that masks which operation failed.
- **Acceptable** (no action): intentional suppression with an explanatory
  comment; failures in optional/telemetry operations; catch blocks in tests;
  cleanup-then-rethrow.
- **Actions**: Critical → fix immediately and rerun tests; Warning → fix if
  straightforward, otherwise note in the PR.

### 7. PR

1. Rebase onto the latest `main` and re-verify:

   ```bash
   git fetch origin main
   git rebase origin/main
   ```

   Rerun the build and tests. If the rebase conflicts, `git rebase --abort`,
   report the conflicting files, and stop.

2. Commit with the conventional format — type is one of
   `feat` / `fix` / `refactor` / `test` / `docs` / `chore`:

   ```
   <type>(<scope>): <description>

   <body explaining what and why>

   Fixes #<id>
   ```

   The `Fixes #<id>` trailer goes on the last line so the merge closes the ticket.

3. Push the branch:

   ```bash
   git push -u origin feature/<id>-<desc>
   ```

4. Open the PR with `gh pr create`, using this body structure:

   ```markdown
   ## Summary
   <1-2 sentences>

   ## Ticket
   Fixes #<id>

   ## Changes
   - <change>

   ## Testing
   <commands run and their results>

   ## Checklist
   - [x] Tests pass
   - [x] Security review done
   - [ ] Documentation updated

   ## Notes
   <Medium/Low security findings, deferred Should-Fix items, or "None">
   ```

5. **UI work**: never commit screenshots to the repo. Host them in a **secret
   GitHub gist** (`gh gist create` without `--public`) and link the raw image
   URLs from a `## Screenshots` section in the PR body.

6. **Never commit to `main`.**

## Explicitly excluded

These are Claude-Code-only ccflow mechanics with no Codex equivalent — Codex owns
every role in one loop, so they do not apply here:

- `Task` subagents (planner / implementer / reviewers).
- `AskUserQuestion` interactive gates.
- `/goal` autopilot and `.plans/` plan files.
- Babysit label automation and the board lifecycle.
- Pencil design tooling.
- `.claude/settings.json`, hooks, and `CLAUDE_PLUGIN_ROOT`.
