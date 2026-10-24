# Phase 4: Implement (Green)

Read this file only when Phase 4 starts. Compact implementation folds the standard delegation into Phase 3 but retains this phase's visual verification and screenshot persistence for UI tickets. If Phase 3 ran parallel or sequential lanes, including dependency waves, skip the standard implementation delegation, then run `## Visual Verification` for a UI ticket (including screenshot persistence) and continue to Phase 5 (`phases/phase-5-refactor.md`): its single `## Completed-Change Verification` runs the former lane barrier, so nothing here runs the full suite. Only concurrency is restricted for UI work, never its required evidence.

Delegate to the `implementer` agent to make failing tests pass.

For ordinary implementation, apply the diagnosis
trigger in the plugin's `docs/model-escalation.md`. After two failed attempts at the
same unexplained defect, the implementer returns its evidence to the orchestrator,
which delegates a bounded diagnosis to `domain-specialist` before the next permitted
attempt. Feed validated advice back to the implementer; retain the three-attempt cap.

## Delegation Context

Pass:

- Worktree path. Tell the agent to follow the implementer's `## Working Directory` section on every command, including its **Never hand-rescue a blocked or stranded edit** rule: a blocked or stranded `Write`/`Edit` is re-issued to the correct `.worktrees/<id>-<desc>/` path, never rescued with `git stash`/`git checkout`/`git apply`. See the `shell-rules` skill for command patterns.
- The plan by path: the absolute main-checkout plan file path `"<repo-root>/.plans/<filename>"` (never a relative `.plans/` path; the file is gitignored and absent inside the worktree) plus the headings to read: `## Ticket Details`, `## Implementation Plan`, and `## Architectural Context`. The plan travels by path, never pasted plan content.
- Phase 3's `## Context Budget` brief, verbatim (`phases/phase-3-test-red.md`): the working budget is about half of the model's advertised context window, automatic compaction is disabled, and a low-context state is reported as `Context: low — …`, never pushed through.
- The failing-test evidence: the absolute path of the red-run log that Phase 3's implementer reported, plus a summary of at most 5 lines (the failing test names and the first assertion message), never the full failure output. If the log is missing or unreadable, the implementer reruns the named failing tests with the resolved scoped command and reports that it did; this is not missing context.
- Return evidence to the orchestrator after two failed attempts at the same unexplained defect, before spending the next attempt; do not delegate diagnosis yourself.
- Attachment paths if relevant.
- LSP diagnostic reminder if configured.
- The resolved Verification command: the single lane's Verification cell (its `Tests:` value), split at `; lint: ` into the scoped test command and the optional scoped lint command, or its `full:` fallback with the reason. Tell the implementer to report with the `Verification (scoped)` or `Verification (full fallback)` format from `## Verification` below; the project's full build, test suite and lint are not passed here, they belong to Phase 5's Completed-Change Verification.
- Resolved topic docs: at most 3 `docs/<topic>.md` paths matched to Files to Modify/Create.
- The plan's `### Documentation Routing` section, verbatim.

**Missing context stop.** A `Missing context:` report stops the run and is reported to the user, never re-delegated with a pasted digest or summary. The worker's first summary line `Missing context: <absolute path> [<section heading>] — <reason>` takes the error gate `Missing context in Phase 4: <path> [<heading>] — <reason>`, whose fix is to repair the plan or run `/cenci:implement <id> replan`. A missing failing-test log is the one exception and never triggers it.

## Rules

- Follow the plan exactly.
- Make tests pass with the simplest correct implementation.
- Consult only the topic docs passed in the Delegation Context above.
- Honor applicable `AGENTS.md` guidance; never read `README.md` wholesale — `Grep` it for the specific contract being touched.
- Do not edit documentation (`README.md`, `AGENTS.md`/`CLAUDE.md`, or `docs/**`) incidentally — Phase 8 owns those, except a doc file explicitly named in the plan's Files to Modify/Create. Instead, emit the `Docs to update:` section in its report, one line per doc or the literal `None.`.
- A doc file named in the plan's `### Files to Modify` / `### Files to Create` is written with its plan-required content in every policy, including `minimal`.
- Name the owner from `### Documentation Routing` on each `Docs to update:` line, never `AGENTS.md` for a contract, catalog, setup detail or procedure.
- No premature abstractions, dead code, commented-out code, or TODOs without ticket references.

## Verification

After implementation:

1. Run the resolved Verification command from the Delegation Context (the lane's scoped test command, or its explicit `full:` fallback).
2. Run the scoped lint command when the cell names one. An absent `lintCommand` skips this step cleanly — no error, no false hard-gate failure. Without a scoped lint command, lint runs only in Phase 5's Completed-Change Verification.
3. Report the exact command and result in one of these literal formats as the result line; failure details and a `Context: low` first line still follow the rules below:
   - `Verification (scoped): <command> — <pass|fail> — not covered: <scope outside the command>`
   - `Verification (full fallback): <command> — <pass|fail> — reason: <reason>`

A scoped pass is never reported as full suite, all tests or CI green. The single full build, test suite and lint run later, once, as Completed-Change Verification (`phases/phase-5-refactor.md`).

A report without a `Verification (scoped)` or `Verification (full fallback)` result line that carries a pass or fail verdict (absent, truncated, in another format, or a `Context: low` stop with no verdict) means the verification did not run: rerun it, and count an attempt toward the 3 only when it ran and failed. A `Context: low` stop still takes the context-budget signal below instead of a rerun. A `Missing context:` first line is not a missing Verification line: it takes the Missing context stop above, never a rerun.

If the command or lint fails, analyze root cause, fix, and rerun. Retry up to 3 times, then stop, reporting exact errors, attempts, and best hypothesis. Lint failures are held to the same hard gate as test failures — no silent pass-through.

**Context-budget signal.** A summary opening with `Context: low`, or a delegation that would need a second resume to finish the same work, is a split signal per Phase 3's `## Context Budget`: report it to the root agent and stop at the error gate `Context budget exceeded in lane <n>: <done>/<remaining>`, recommending `/cenci:refine <id>` (or `/cenci:refine <parentId>` for a split child). Never retry with a longer lane or a third delegation.

## Visual Verification

For frontend plans with visual components, read the `verify-ui` reference skill and
follow its shared core (screenshot capture, fix-before-proceeding, never-silently-skip).

Fix significant discrepancies or get explicit user acceptance before Phase 5.

### Persist Screenshots For The PR

If `isUiTicket` is true, save a final screenshot of every affected screen/state to `${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/` with descriptive kebab-case filenames suffixed by locale — **both `en-US` and `it-IT` for every capture** (e.g. `login-form-error-state-en-US.png`, `login-form-error-state-it-IT.png`) — plus the component gallery page in both locales (`gallery-en-US.png`, `gallery-it-IT.png`). Phase 6 + 7's `## Acceptance Review` requires exactly these four kinds of evidence for a UI ticket; a missing locale or a missing gallery capture fails the acceptance review, never silently passes it. Capture with `playwright-cli screenshot` against the running dev build, or copy the relevant Playwright Test `toHaveScreenshot` output. Capture **after** visual verification passes so the images show the final state — Phase 9 uploads them and embeds them in the PR body as review aids. If no browser tooling is available, skip this step; Phase 9 will note the gap in the PR.
