# Phase 4: Implement (Green)

Read this file only when Phase 4 starts. Skip this separate phase if Phase 3 is running approved compact implementation mode. If Phase 3 ran Parallel Lanes, skip everything except the `## Lane Verification Barrier` section at the end — the lanes already reached green on their scoped tests, and lanes never run for UI tickets, so the Visual Verification section does not apply.

Delegate to the `implementer` agent to make failing tests pass.

## Delegation Context

Pass:

- Worktree path. Tell the agent: target the worktree explicitly on every command — via `git -C <worktree-path>` for git commands, absolute paths for file operations, or the client's working-directory option — do **not** prefix every command with `cd <path> &&` (a `cd … && git …` compound can never be auto-approved; use `git -C <path>` if git must target another directory). If a `Write`/`Edit` is blocked or stranded, re-issue the same edit to the correct `.worktrees/<id>-<desc>/` path — never hand-rescue it with `git stash`/`git checkout`/`git apply`. See the `shell-rules` skill for command patterns.
- Plan file sections: `## Ticket Details`, `## Implementation Plan`, and `## Architectural Context`.
- The failing tests and their failure output.
- Attachment paths if relevant.
- LSP diagnostic reminder if configured.
- The project's `buildCommand`, `testCommand`, and `lintCommand` (when set).

## Rules

- Follow the plan exactly.
- Make tests pass with the simplest correct implementation.
- Consult only relevant `docs/<topic>.md` files.
- Honor `CLAUDE.md` and `README.md`; update docs if behavior, setup, configuration, or user-visible contracts change.
- No premature abstractions, dead code, commented-out code, or TODOs without ticket references.

## Verification

After implementation:

1. Run the full build.
2. Run the full test suite.
3. Run lint (when `lintCommand` is set). An absent `lintCommand` skips this step cleanly — no error, no false hard-gate failure.
4. Report exact commands and results.

If build/tests/lint fail, analyze root cause, fix, and rerun. Retry up to 3 times, then stop, reporting exact errors, attempts, and best hypothesis. Lint failures are held to the same hard gate as build/test failures — no silent pass-through.

## Visual Verification

For frontend plans with visual components, read the `verify-ui` reference skill and
follow its shared core (screenshot capture, fix-before-proceeding, never-silently-skip).

Fix significant discrepancies or get explicit user acceptance before Phase 5.

### Persist Screenshots For The PR

If `isUiTicket` is true, save a final screenshot of every affected screen/state to `${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/` with descriptive kebab-case filenames (e.g. `login-form-error-state.png`). Capture with `playwright-cli screenshot` against the running dev build, or copy the relevant Playwright Test `toHaveScreenshot` output. Capture **after** visual verification passes so the images show the final state — Phase 9 uploads them and embeds them in the PR body as review aids. If no browser tooling is available, skip this step; Phase 9 will note the gap in the PR.

## Lane Verification Barrier

Runs only when Phase 3 ran Parallel Lanes, after every lane has reported red→green. This is the single authoritative verification for the whole change — lane implementers deliberately never run the full suite (see Phase 3's `## Parallel Lanes`), so nothing before this point has proven the lanes work together.

Delegate one `implementer` with the standard Delegation Context above (worktree path, plan sections, build/test/lint commands) plus each lane's `Scope:` summary, instructed to:

1. Run the full build.
2. Run the full test suite.
3. Run lint (when `lintCommand` is set; an absent `lintCommand` skips cleanly).
4. Fix any cross-lane integration failure — this delegation may touch any file, the per-lane file restriction no longer applies.
5. Report exact commands and results, including which failures were cross-lane integration issues.

This barrier is held to the same hard gate as this phase's Verification section: analyze root cause, fix, rerun; after 3 failed attempts, stop and report exact errors, attempts, and best hypothesis. Do not proceed to Phase 5 until build, full test suite, and lint all pass here.
