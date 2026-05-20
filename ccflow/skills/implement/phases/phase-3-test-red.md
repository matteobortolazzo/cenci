# Phase 3: Test First (Red)

Read this file only when Phase 3 starts.

Delegate to the `implementer` agent to write tests first. Tests should fail.

## Compact Implementation

If `ccflow.compactImplementation` is true and the plan is small, low-risk, and concrete, Phase 3, 4, and 5 may be combined into one implementer delegation. The implementer must still:

1. Write tests first.
2. Run them and report failing test names and failure reasons.
3. Implement the feature.
4. Refactor only touched code.
5. Run full build and tests and report results.

Do not use compact mode for auth, payment, security-sensitive code, data migrations, broad refactors, large UI work, flaky test infrastructure, or unclear requirements.

## Delegation Context

Pass:

- Worktree path. First Bash command must be `cd <worktree-path>`.
- Plan file sections: `## Ticket Details`, `## Implementation Plan`, and `## Architectural Context`.
- Files to modify/create and planner notes.
- Acceptance criteria, edge cases, and error scenarios.
- Attachment paths if relevant.
- Design Components table and Design Tokens if `designSpec` was loaded.

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

## Quality Rules

Tests must assert behavior and business rules: status codes, response shapes, state changes, visible UI states, error behavior. Do not assert call counts, implementation details, hardcoded magic values, or copied implementation outputs.

Tests must be readable, deterministic, and independent.

If any new test passes before implementation, investigate whether it actually covers new behavior.

## Error Recovery

If tests cannot be written, identify the blocker. Fix missing test infrastructure or dependencies when safe. If requirements are unclear, stop and ask the user.
