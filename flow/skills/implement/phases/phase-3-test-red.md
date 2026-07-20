# Phase 3: Test First (Red)

Read this file only when Phase 3 starts.

Delegate to the `implementer` agent to write tests first. Tests should fail.

## Compact Implementation

If `cenci.compactImplementation` is true and the plan is small, low-risk, and concrete, Phase 3, 4, and 5 may be combined into one implementer delegation. The implementer must still:

1. Write tests first.
2. Run them and report failing test names and failure reasons.
3. Implement the feature.
4. Refactor only touched code.
5. Run full build and tests and report results.

Do not use compact mode for auth, payment, security-sensitive code, data migrations, broad refactors, large UI work, flaky test infrastructure, or unclear requirements.

## Parallel Lanes

If the plan file's `## Implementation Plan` contains a `### Parallel Lanes` section, Phases 3 and 4 may fan out one implementer per lane. Quality gates are unchanged: per-lane red-before-green discipline, a single authoritative full-suite verification barrier (Phase 4's `## Lane Verification Barrier`), and the full Phase 5–9 flow (one refactor pass, all reviewers, one PR).

**Eligibility re-check (main agent, before any fan-out).** The planner's declaration is a proposal, not authorization. Verify all of the following against the plan file; if any check fails, ignore the lanes section entirely and run the standard sequential Phase 3 → Phase 4 flow — never partially:

- Every file across all lanes' `Files:` lists appears in exactly one lane. Any overlap → sequential.
- `isUiTicket` is false. UI work always takes the sequential path (design pre-read and visual verification live in the standard Phase 4).
- No lane file matches the sensitive-path pattern set from `SKILL.md`'s `## Sensitive-path backstop` (built-in defaults unioned with `security.sensitivePaths`). Any match → sequential.
- Each lane declares `Files:`, `Tests:`, and `Scope:`. A malformed lane → sequential.

**Fan-out.** One implementer delegation per lane, each receiving the standard Delegation Context below plus its lane's `Files:`, `Tests:`, and `Scope:`. Each lane implementer must, in order:

1. Write the lane's tests first. Tests should fail.
2. Run only the lane-scoped tests (`Tests:` command/pattern) and report failing test names and failure reasons — the observed red comes before any implementation.
3. Implement the lane to make its failing tests pass, then re-run the lane-scoped tests to green.
4. Touch only files in the lane's `Files:` list. If correctness genuinely requires a file outside the lane, stop the lane and report — do not edit it.
5. Never run the full build or full test suite — concurrent full runs race in the shared worktree (build caches, ports, lockfiles). The full suite runs exactly once, in Phase 4's `## Lane Verification Barrier`.

If the resolved config has `cenci.implementerConcurrency: "sequential"`, run the same lane delegations one after another instead of concurrently; otherwise (default `"parallel"`) launch them together. The lane structure, rules, and gates are identical in both modes.

**Error gate (restated for the parallel risk profile — see `docs/pipeline-safety.md`).** A lane implementer applies the same analyze-fix-rerun loop as Phase 4, up to 3 attempts within its lane. If any lane still fails after that (red tests that cannot be written, green that cannot be reached, or a needed out-of-lane file), let already-running lanes finish, then clear the Goal Autopilot (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`) and stop, reporting per-lane status: which lanes completed red→green, which failed, exact errors and attempts. Completed lanes' work stays in the worktree — do not revert it; the user decides whether to continue sequentially or abort. Do not proceed to the Lane Verification Barrier with a failed lane.

When lanes run, skip the single-implementer delegation below and Phase 4's standard delegation — Phase 4 runs only its `## Lane Verification Barrier`.

## Delegation Context

Pass:

- Worktree path. Tell the agent: target the worktree explicitly on every command — via `git -C <worktree-path>` for git commands, absolute paths for file operations, or the client's working-directory option — do **not** prefix every command with `cd <path> &&`. See the `shell-rules` skill for command patterns.
- Plan file sections: `## Ticket Details`, `## Implementation Plan`, and `## Architectural Context`.
- Files to modify/create and planner notes.
- Acceptance criteria, edge cases, and error scenarios.
- Attachment paths if relevant.
- Design Components table and Design Tokens from the plan file's `## Design Context` section, if present.

## Test Priorities

Frontend:

- Read the `testing` skill's UI Component Classification section.
- Critical journeys: E2E first.
- Smart/container, form-heavy, and data display: integration/component tests.
- Unit tests only for complex service logic, validators, parsing, calculations, or state machines.
- Visual/layout work: note required visual verification for Phase 4.

Backend:

- Prefer integration tests for real flows.
- Unit tests only for complex domain logic, calculations, state machines, validation, or parsing.

Unit-test bar (both frontend and backend): default to zero unit tests. Every unit test written must be justified by what it catches that an integration test cannot reasonably catch; an unjustifiable unit test is skipped, not written. Coverage means acceptance criteria exercised through the real stack, not unit-test count.

## Quality Rules

Tests must assert behavior and business rules: status codes, response shapes, state changes, visible UI states, error behavior. Do not assert call counts, implementation details, hardcoded magic values, or copied implementation outputs.

Tests must be readable, deterministic, and independent.

If any new test passes before implementation, investigate whether it actually covers new behavior.

## Error Recovery

If tests cannot be written, identify the blocker. Fix missing test infrastructure or dependencies when safe. If requirements are unclear, clear the Goal Autopilot first (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`), then stop and ask the user via `AskUserQuestion`.
