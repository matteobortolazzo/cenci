# Phase 3: Test First (Red)

Read this file only when Phase 3 starts.

Apply the implementation diagnosis trigger in the plugin's `docs/model-escalation.md`
to compact implementation and parallel lanes as well as ordinary test work. Tell
implementers to return evidence after two unsuccessful attempts at the same unexplained
defect; expected red tests are not failed repair attempts. The orchestrator delegates
the bounded diagnosis to `domain-specialist` and feeds back only validated advice.
Existing retry limits and the lane verification barrier still apply.

Delegate to the `implementer` agent to write tests first. Tests should fail.

## Delegation Preflight Re-check

Immediately before the first implementation mutation — before any implementer is delegated, in every mode — re-read the plan file's `### Delegation Preflight` (under `## Implementation Plan`) and confirm it against the current plan: every lane has an exclusive owned-file set that overlaps no other lane and a verification command; every retained lane records a concrete ineligibility reason; **every lane is at most one M** (its owned files fit <= 30 changed files and its scope fits <= 2,000 changed lines — `docs/ticket-sizing.md`); and a plan whose `### Size Estimate` is `L` inventories **at least two delegated lanes** — an L ticket is always several lanes with exclusive file ownership, never one lane. If the section is missing, incomplete, stale against `### Files to Modify`/`### Files to Create`, ambiguous, or assigns overlapping files, remain read-only and stop at an error gate returning the ticket to planning (`/cenci:implement <id> replan`); never infer the missing decision from prior chat. A lane larger than M, or a single-lane L, is a **split signal**, not a lane to widen: stop at the same error gate recommending `/cenci:refine <id>` (or `/cenci:refine <parentId>` for a split child) — the ticket is one size larger than written.

## Context Budget

Every implementer delegation in this phase and the next — single implementer, compact mode, each parallel or sequential lane, and the Lane Verification Barrier — is briefed with the working budget, verbatim in its prompt:

> **Context budget.** Your working budget is about half of your model's advertised context window — plan to finish this lane inside it; the window's ceiling is not a sizing unit. Automatic compaction is disabled (`autoCompactDisabled` in `.cenci/config.json`), so a session that reaches its window stops rather than degrading gracefully. If you judge you are past roughly half your window with work remaining, stop at a clean point (tests green, or clearly red with the failure named), and make the first line of your summary `Context: low — done: <what>; remaining: <what>`. Never push through a low-context state, and never ask for a longer lane.

**The signal is a split signal.** A worker summary opening with `Context: low`, or a lane that needs a **second resume** (a second fresh delegation to finish the same lane after one resume already happened), is evidence the ticket is one size larger than written (`docs/ticket-sizing.md`, "Lane size and context budget"). The orchestrating agent reports it to the root agent as a split signal and stops at an error gate — `Context budget exceeded in lane <n>: <done>/<remaining>` — recommending `/cenci:refine <id>` (or `/cenci:refine <parentId>` for a split child) to re-size and split, and updating the preflight on the next planning pass. It is **never** retried with a longer lane, a merged lane, a raised budget, or a third delegation for the same lane. Completed lanes' work stays in the worktree; the user decides whether to continue by hand or abort.

## Compact Implementation

If `cenci.compactImplementation` is true and the plan is small, low-risk, and concrete, Phase 3, 4, and 5 may be combined into one implementer delegation. The implementer must still:

1. Write tests first.
2. Run them and report failing test names and failure reasons.
3. Implement the feature.
4. Refactor only touched code.
5. Run Phase 5's `## Reuse Check` — read that one section of `phases/phase-5-refactor.md` (that section only, not the whole file) and follow its steps and cost guards inside this same delegation. Folding Phase 5 away does not drop this check: it is a handful of searches over code already in hand, and it is the only step in the pipeline that sees duplication this change introduces against code outside the diff.
6. Run full build and tests and report results.

Do not use compact mode for auth, payment, security-sensitive code, data migrations, broad refactors, large UI work, flaky test infrastructure, or unclear requirements.

## Parallel Lanes

If the plan file's `## Implementation Plan` contains a `### Parallel Lanes` section, Phases 3 and 4 may fan out one implementer per lane. Quality gates are unchanged: per-lane red-before-green discipline, a single authoritative full-suite verification barrier (Phase 4's `## Lane Verification Barrier`), and the full Phase 5–9 flow (one refactor pass, all reviewers, one PR).

**Eligibility re-check (main agent, before any fan-out).** The planner's declaration is a proposal, not authorization. Verify all of the following against the plan file; if any check fails, ignore the lanes section entirely and run the standard sequential Phase 3 → Phase 4 flow — never partially:

- Every file across all lanes' `Files:` lists appears in exactly one lane. Any overlap → sequential.
- `isUiTicket` is false. UI work always takes the sequential path (visual verification lives in the standard Phase 4).
- No lane file matches the sensitive-path pattern set from `SKILL.md`'s `## Sensitive-path backstop` (built-in defaults unioned with `security.sensitivePaths`). Any match → sequential.
- Each lane declares `Files:`, `Tests:`, and `Scope:`. A malformed lane → sequential.
- Every lane's `Files:` list fits one M (<= 30 files) and the lane's `Scope:` fits <= 2,000 changed lines. An over-M lane is **not** a sequential fallback — it is the split signal `## Delegation Preflight Re-check` above stops on.

**Sequential lanes.** When the plan's `### Delegation Preflight` inventories two or more delegated lanes but fan-out is disqualified here (a UI ticket, a sensitive-path match, or `cenci.implementerConcurrency: "sequential"`), still run **one implementer delegation per lane, in lane order** — each with the standard Delegation Context plus its lane's owned files, tests and scope, each red→green on its lane-scoped tests, each at most one M — then Phase 4's `## Lane Verification Barrier`. Disqualification changes concurrency, never lane size: an L ticket never collapses into one implementer delegation.

**Fan-out.** One implementer delegation per lane, each receiving the standard Delegation Context below (including the `## Context Budget` brief) plus its lane's `Files:`, `Tests:`, and `Scope:`. Each lane implementer must, in order:

1. Write the lane's tests first. Tests should fail.
2. Run only the lane-scoped tests (`Tests:` command/pattern) and report failing test names and failure reasons — the observed red comes before any implementation.
3. Implement the lane to make its failing tests pass, then re-run the lane-scoped tests to green.
4. Touch only files in the lane's `Files:` list. If correctness genuinely requires a file outside the lane, stop the lane and report — do not edit it.
5. Never run the full build or full test suite — concurrent full runs race in the shared worktree (build caches, ports, lockfiles). The full suite runs exactly once, in Phase 4's `## Lane Verification Barrier`.

If the resolved config has `cenci.implementerConcurrency: "sequential"`, run the same lane delegations one after another instead of concurrently; otherwise (default `"parallel"`) launch them together. The lane structure, rules, and gates are identical in both modes.

**Error gate (restated for the parallel risk profile — see `docs/pipeline-safety.md`).** A lane implementer applies the same analyze-fix-rerun loop as Phase 4, up to 3 attempts within its lane. If any lane still fails after that (red tests that cannot be written, green that cannot be reached, or a needed out-of-lane file), let already-running lanes finish, then stop, reporting per-lane status: which lanes completed red→green, which failed, exact errors and attempts. A lane reporting `Context: low`, or needing a second resume, takes the `## Context Budget` split-signal stop instead — it is reported, never retried with a longer lane. Completed lanes' work stays in the worktree — do not revert it; the user decides whether to continue sequentially or abort. Do not proceed to the Lane Verification Barrier with a failed lane.

When lanes run, skip the single-implementer delegation below and Phase 4's standard delegation — Phase 4 runs only its `## Lane Verification Barrier`.

## Delegation Context

Pass:

- Worktree path. Tell the agent: target the worktree explicitly on every command — via `git -C <worktree-path>` for git commands, absolute paths for file operations, or the client's working-directory option — do **not** prefix every command with `cd <path> &&`. See the `shell-rules` skill for command patterns.
- Plan file sections: `## Ticket Details`, `## Implementation Plan`, and `## Architectural Context`.
- The `## Context Budget` brief above, verbatim, plus this delegation's lane (its owned files and scope from `### Delegation Preflight`; the whole plan when the preflight has one delegated lane).
- Files to modify/create and planner notes.
- Acceptance criteria, edge cases, and error scenarios.
- Attachment paths if relevant.
- Resolved topic docs: at most 3 `docs/<topic>.md` paths matched to Files to Modify/Create.
- On a resume attach route, the `## Resume Brief` below.

## Resume Brief

Applies only when Phase 2 took a resume attach route (it printed the `Resuming #<id> from stage <stage> in ...` line) and the stage routed here: `plan_approved` or `executed`, or the clean-worktree fallback of `reviewed`/`finalized`. A crashed run's work is almost always uncommitted in the worktree — the pipeline commits only in Phase 9 — so the dirty worktree is the progress signal; never stash or discard it.

If the worktree is clean (no output from `git -C <worktree-path> status --short`, no commits from `git -C <worktree-path> log --oneline main..HEAD`), print `Resumed worktree is clean — running Phase 3 with the standard brief` and delegate with the standard Delegation Context, no resume brief.

Otherwise, gather the three outputs with `git -C <worktree-path> status --short`, `git -C <worktree-path> diff --stat HEAD` and `git -C <worktree-path> log --oneline main..HEAD`, and add this to the Delegation Context:

```text
Resume brief — an earlier run of this ticket crashed partway through implementation
and left this worktree as it is. Nothing is committed unless it appears in the log below.

git status --short:
<output>

git diff --stat HEAD:
<output>

git log --oneline main..HEAD:
<output>

Before implementing any plan task, check which plan tasks are already done in the
worktree (the files above and the tests that already exist) and continue from what
exists; do not redo finished work. Never stash, reset, checkout over or discard these
changes, and do not stage or commit them.
```

The brief goes to every lane — the single implementer, each parallel or sequential lane (`## Parallel Lanes`), and the compact implementer (`## Compact Implementation`). It overrides the per-lane "write the lane's tests first; tests should fail" order only where a task is already done: tests that already exist and pass because the implementation is present are expected on a resume, not a red-phase violation. Tasks that are not yet done follow the normal red-before-green order.

A crash-resume re-brief is not a second resume for `## Context Budget`'s split signal: that count is per session, and the crash was a memory kill, not context exhaustion.

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

If tests cannot be written, identify the blocker. Fix missing test infrastructure or dependencies when safe. If requirements are unclear, stop and ask the user via `AskUserQuestion`.
