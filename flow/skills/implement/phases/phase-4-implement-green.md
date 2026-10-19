# Phase 4: Implement (Green)

Read this file only when Phase 4 starts. Compact implementation folds the standard delegation into Phase 3 but retains this phase's visual verification and screenshot persistence for UI tickets. If Phase 3 ran parallel or sequential lanes, including dependency waves, skip the standard implementation delegation: run `## Lane Verification Barrier`, then run `## Visual Verification` for a UI ticket (including screenshot persistence). Only concurrency is restricted for UI work, never its required evidence.

Delegate to the `implementer` agent to make failing tests pass.

For ordinary implementation and the Lane Verification Barrier, apply the diagnosis
trigger in the plugin's `docs/model-escalation.md`. After two failed attempts at the
same unexplained defect, the implementer returns its evidence to the orchestrator,
which delegates a bounded diagnosis to `domain-specialist` before the next permitted
attempt. Feed validated advice back to the implementer; retain the three-attempt cap.

## Delegation Context

Pass:

- Worktree path. Tell the agent: target the worktree explicitly on every command — via `git -C <worktree-path>` for git commands, absolute paths for file operations, or the client's working-directory option — do **not** prefix every command with `cd <path> &&` (a `cd … && git …` compound can never be auto-approved; use `git -C <path>` if git must target another directory). If a `Write`/`Edit` is blocked or stranded, re-issue the same edit to the correct `.worktrees/<id>-<desc>/` path — never hand-rescue it with `git stash`/`git checkout`/`git apply`. See the `shell-rules` skill for command patterns.
- Plan file sections: `## Ticket Details`, `## Implementation Plan`, and `## Architectural Context`.
- Phase 3's `## Context Budget` brief, verbatim (`phases/phase-3-test-red.md`): the working budget is about half of the model's advertised context window, automatic compaction is disabled, and a low-context state is reported as `Context: low — …`, never pushed through.
- The failing tests and their failure output.
- Return evidence to the orchestrator after two failed attempts at the same unexplained defect, before spending the next attempt; do not delegate diagnosis yourself.
- Attachment paths if relevant.
- LSP diagnostic reminder if configured.
- The project's `buildCommand`, `testCommand`, and `lintCommand` (when set).
- Resolved topic docs: at most 3 `docs/<topic>.md` paths matched to Files to Modify/Create.

## Rules

- Follow the plan exactly.
- Make tests pass with the simplest correct implementation.
- Consult only the topic docs passed in the Delegation Context above.
- Honor applicable `AGENTS.md` guidance; never read `README.md` wholesale — `Grep` it for the specific contract being touched.
- Do not edit documentation (`README.md`, `AGENTS.md`/`CLAUDE.md`, or `docs/**`) incidentally — Phase 8 owns those, except a doc file explicitly named in the plan's Files to Modify/Create. Instead, emit the `Docs to update:` section in its report, one line per doc or the literal `None.`.
- No premature abstractions, dead code, commented-out code, or TODOs without ticket references.

## Verification

After implementation:

1. Run the full build.
2. Run the full test suite.
3. Run lint (when `lintCommand` is set). An absent `lintCommand` skips this step cleanly — no error, no false hard-gate failure.
4. Report exact commands and results.

If build/tests/lint fail, analyze root cause, fix, and rerun. Retry up to 3 times, then stop, reporting exact errors, attempts, and best hypothesis. Lint failures are held to the same hard gate as build/test failures — no silent pass-through.

**Context-budget signal.** A summary opening with `Context: low`, or a delegation that would need a second resume to finish the same work, is a split signal per Phase 3's `## Context Budget`: report it to the root agent and stop at the error gate `Context budget exceeded in lane <n>: <done>/<remaining>`, recommending `/cenci:refine <id>` (or `/cenci:refine <parentId>` for a split child). Never retry with a longer lane or a third delegation.

## Visual Verification

For frontend plans with visual components, read the `verify-ui` reference skill and
follow its shared core (screenshot capture, fix-before-proceeding, never-silently-skip).

Fix significant discrepancies or get explicit user acceptance before Phase 5.

### Persist Screenshots For The PR

If `isUiTicket` is true, save a final screenshot of every affected screen/state to `${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/` with descriptive kebab-case filenames suffixed by locale — **both `en-US` and `it-IT` for every capture** (e.g. `login-form-error-state-en-US.png`, `login-form-error-state-it-IT.png`) — plus the component gallery page in both locales (`gallery-en-US.png`, `gallery-it-IT.png`). Phase 6 + 7's `## Acceptance Review` requires exactly these four kinds of evidence for a UI ticket; a missing locale or a missing gallery capture fails the acceptance review, never silently passes it. Capture with `playwright-cli screenshot` against the running dev build, or copy the relevant Playwright Test `toHaveScreenshot` output. Capture **after** visual verification passes so the images show the final state — Phase 9 uploads them and embeds them in the PR body as review aids. If no browser tooling is available, skip this step; Phase 9 will note the gap in the PR.

## Lane Verification Barrier

Runs when Phase 3 ran parallel or sequential lanes, including dependency waves, after every lane has reported red→green and all workers have finished. This is the single authoritative verification for the whole change — lane implementers deliberately never run the full suite (see Phase 3's `## Parallel Lanes`), so nothing before this point has proven the lanes work together. Run the barrier once after all waves, not once per wave; required re-verification after a fix or later refactor still applies.

Include the return-after-two-failed-attempts instruction in this fresh implementer's
prompt. Before its next permitted attempt, the orchestrator applies the diagnosis
trigger in the plugin's `docs/model-escalation.md` and delegates to `domain-specialist`.
Validate the advice and return it to the implementer without extending the three-attempt cap.

Delegate one `implementer` with the standard Delegation Context above (worktree path, plan sections, build/test/lint commands, the `## Context Budget` brief) plus each lane's `Scope:` summary, instructed to:

1. Run the full build.
2. Run the full test suite.
3. Run lint (when `lintCommand` is set; an absent `lintCommand` skips cleanly).
4. Fix any cross-lane integration failure — this delegation may touch any file, the per-lane file restriction no longer applies.
5. Report exact commands and results, including which failures were cross-lane integration issues.

This barrier is held to the same hard gate as this phase's Verification section: analyze root cause, fix, rerun; after 3 failed attempts, stop and report exact errors, attempts, and best hypothesis. Do not proceed to Phase 5 until build, full test suite, and lint all pass here.
