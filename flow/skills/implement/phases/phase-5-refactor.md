# Phase 5: Refactor

Read this file only when Phase 5 starts. Skip this separate phase if Phase 3 is running approved compact implementation mode — with one carve-out: the `## Reuse Check` below still runs, folded into that single implementer delegation. Compact mode reaches it by reading that one section directly: Phase 3's `## Compact Implementation` checklist sends the implementer here for it, so the check's steps and cost guards are defined in exactly one place and neither path runs it from memory. It is a handful of searches over code the agent is already holding, so skipping it saves nothing worth the gap it opens.

Delegate to the `implementer` agent for focused cleanup of touched code only.

## Process

Pass:

- Worktree path. Tell the agent to follow the implementer's `## Working Directory` section on every command. See the `shell-rules` skill for command patterns.
- The plan by path: the absolute main-checkout plan file path `"<repo-root>/.plans/<filename>"` (never a relative `.plans/` path; the file is gitignored and absent inside the worktree) plus the heading to read, `## Implementation Plan`: refactors must not override plan decisions (`docs/plan-fidelity.md`). The plan travels by path, never pasted plan content. A `Missing context:` report stops the run and is reported to the user, never re-delegated with a pasted digest or summary.
- Changed file list.
- LSP diagnostic reminder if configured.
- The scoped Verification command of the lane (every lane's cell in lane mode), with its optional scoped lint command, as resolved in Phase 3's Delegation Context.
- The project's `buildCommand`, `testCommand` and `lintCommand` (when set), for `## Completed-Change Verification` below: the refactor worker receives them only in ordinary mode, where Completed-Change Verification is its last step; in lane mode they go only to the barrier worker.
- Resolved topic docs: at most 3 `docs/<topic>.md` paths matched to Files to Modify/Create.

**Missing context stop.** The worker's first summary line `Missing context: <absolute path> [<section heading>] — <reason>` takes the error gate `Missing context in Phase 5: <path> [<heading>] — <reason>`, whose fix is to repair the plan or run `/cenci:implement <id> replan`. It covers the refactor worker, the Lane Verification Barrier worker and compact mode. It is never absorbed as an ordinary failure by a hard-gate retry.

Review changed code for:

- Dead code or unnecessary abstractions.
- Duplicated logic; consolidate only when used 3+ times or clearly established locally.
- Unclear names.
- Complex conditionals that can be simplified.
- Overly clever code.

That duplication bullet governs duplication that was *already there* and this change merely touched. Duplication this change **introduces** is governed by `## Reuse Check` below — run it as part of the same delegation, not a separate pass.

After each cleanup step, run the scoped Verification command of the lane that owns the changed file (and its scoped lint command when the cell names one) and report it as `Verification (scoped): …` with what it did not cover. A changed file that no lane owns is reported under `not covered:`; `## Completed-Change Verification` covers it. The full build, test suite and lint do not run per step: Completed-Change Verification runs once all cleanup is done — including the `## Reuse Check` below. Behavior must not change.

## Reuse Check

Everything above scopes to touched code, which leaves one blind spot: a helper, constant, or fixture added by this change that already exists elsewhere in the repo looks perfectly clean from inside the diff. Nothing in the pipeline sees it — Phase 4 only cares that tests pass, and the Phase 6 + 7 reviewers read the same diff. Close that specific gap here, cheaply. This is a targeted check on new code, **never a repo-wide duplication sweep** — that is `/cenci:refactor`'s job, it fans out dedicated analyzers over a whole scope and emits tickets rather than fixes, and pulling it into every ticket's pipeline is not the trade this step makes.

Tell the implementer to run these steps inside the same delegation:

1. List the **named units this diff adds** — new functions, methods, helpers, exported constants, test fixtures, and setup helpers. Additions only: an edit or rename of an existing unit does not count.
2. **Skip the rest of this check entirely when that list is empty.** Pure edits, deletions, and config/data-only diffs are the common case and must cost nothing.
3. For at most the 10 largest added units (by body size), search for an existing equivalent on a behavior-bearing name fragment *and* on a distinctive line from the body — a re-implementation rarely reuses the exact name, so a name-only search is the one that misses. That is two searches per unit, so **at most 20 searches for the whole check**. Restrict the search to the affected project's directory rather than the whole tree; in a monorepo an equivalent in a sibling project is usually not reusable anyway. Derive that directory from the changed file list this phase already receives, not from any earlier phase's state — Phase 2's baseline gate resolves `projects[].slug` values, not paths, and it is skipped entirely on configs with no `gateCommand` and no `projects[]`, so nothing upstream is guaranteed to hand a path down. Take the longest `projects[].path` in the resolved config that prefixes the changed files; when the config declares no `projects[]`, or the changed files sit under no declared path, fall back to the deepest single directory that contains all of them. Never fall back to the whole tree.
4. On a hit, prefer the existing unit: call it, or widen it when the new use needs one more parameter or one more case. Consolidate here even at **two** occurrences — the rule-of-three threshold above exists because rewriting settled code is risky, and that reasoning does not apply when the second occurrence is the one being written right now and is free to simply not exist.
5. When the existing equivalent cannot be reused **without changing behavior for its current callers**, keep the new code and report the near-duplicate in one line in this phase's summary, opened with the literal prefix `Reuse Check:` so Phase 6 + 7 can carry it forward verbatim into `$RUN_DIR/reuse-notes.txt` (see that phase's `## Shared Context`) and Phase 9 can render it without re-deriving anything. Do not rewire the existing unit's other callers to make it fit — that is outside the ticket's scope. A near-duplicate left in place is a refactor/tech-debt observation, so it lands under Phase 9's `### Considered and discarded` and is **never** tracked or turned into a Followup ticket — the same policy the Phase 6 + 7 reviewers already apply to their own refactor findings.

Any consolidation made here is part of this phase's changes: the Completed-Change Verification below covers it, and the same Error Recovery below applies to it.

## Completed-Change Verification

The one authoritative full check of the whole change, defined only here. It runs the full build, the full test suite and lint (when `lintCommand` is set), once, after cleanup and `## Reuse Check`, in both ordinary and lane modes. It absorbs the former lane barrier and this phase's former separate full-suite pass, so no other full verification group runs between the end of implementation or refactor and Phase 8. Phase 2's baseline gate and Phase 9's post-rebase full run are separate and unchanged. An absent `lintCommand` skips the lint step cleanly — no error, no false hard-gate failure.

- **Ordinary mode:** Completed-Change Verification is the last step of the refactor delegation, after cleanup and the Reuse Check.
- **Compact mode:** Phase 3's `## Compact Implementation` step 6 is this Completed-Change Verification; nothing else runs a full group before the acceptance review.
- **Lane mode:** see `### Lane mode: Lane Verification Barrier` below.
- A refactor pass that changes no file still runs exactly once; no second full run follows it.

**Hard gate.** If the build, tests or lint fail, analyze the root cause, fix, and rerun. Retry up to 3 attempts, then stop and report the exact errors, the attempts, and the best hypothesis. Never proceed to the acceptance review while red. Lint failures are held to the same gate — no silent pass-through. Apply the diagnosis trigger in the plugin's `docs/model-escalation.md`: after two failed attempts at the same unexplained defect, the implementer returns its evidence, the orchestrator delegates a bounded diagnosis to `domain-specialist` before the next permitted attempt, and validated advice goes back to the implementer without extending the 3-attempt cap.

**Context Budget.** Brief this delegation with Phase 3's `## Context Budget` verbatim. A worker summary opening with `Context: low` takes that section's split-signal stop; it is never retried with a longer lane.

**Reporting and timing.** Report `Verification (full): <command> — <pass|fail>`. Record the run under the existing `full-checks-<slug>` timing label (`skills/implement/timing.md`) so repeat counts stay meaningful.

A report without a `Verification (full): <command> — pass` line (absent, truncated, in another format, or a `Context: low` stop with no verdict) means the check did not run: rerun it, and count an attempt toward the 3 attempts only when it ran and failed. A `Context: low` stop still takes the split-signal stop above instead of a rerun. A `Missing context:` first line is not a missing Verification line: it takes the Missing context stop above, never a rerun.

**Evidence.** A green Completed-Change Verification is session-local evidence, never persisted across sessions: a resumed run always runs it again. The evidence is stale after any later fix delegation whose report lists a changed file; a report that lists no changed file leaves it valid; a report that is ambiguous or missing counts as stale (fail closed). Every fix-delegation report must carry a literal `Changed files:` line (a file list, or `Changed files: none`); a report without that line, and any edit the root agent applies itself, counts as stale. Phase 6 + 7's `## Completed-Change Verification Re-run` owns the stale-evidence rerun. Phase 8 documentation edits made after a green run are covered by Phase 9's post-rebase full run; no extra full run is added for them.

### Lane mode: Lane Verification Barrier

Applies when Phase 3 ran parallel or sequential lanes, including dependency waves. Lane implementers deliberately never run the full suite (see Phase 3's `## Parallel Lanes`), so nothing before this point has proven the lanes work together. Run the barrier once, after all waves and after the refactor delegation returns — not once per wave.

Delegate one fresh implementer with the standard Delegation Context (worktree path, the absolute plan path with `## Implementation Plan`, the project's build/test/lint commands, the `## Context Budget` brief) plus each lane's `Scope:` summary, instructed to:

1. Run the full build.
2. Run the full test suite.
3. Run lint (when `lintCommand` is set; an absent `lintCommand` skips cleanly).
4. Fix any cross-lane integration failure — this delegation may touch any file, the per-lane file restriction no longer applies.
5. Report the exact commands and results in the `Verification (full)` format, including which failures were cross-lane integration issues.

The hard gate above applies unchanged: analyze root cause, fix, rerun; after 3 attempts, stop and report exact errors, attempts, and best hypothesis. Include the instruction to return evidence after two failed attempts in this implementer's prompt; before its next permitted attempt the orchestrator applies the diagnosis trigger and delegates to `domain-specialist`, then returns the validated advice. Do not proceed to Phase 6 + 7 until build, full test suite and lint all pass here. A `Missing context:` first line from this worker takes the Missing context stop above, never the hard gate retry.

## Error Recovery

If tests fail, identify the specific refactoring step that broke behavior, revert that step only, and try a simpler cleanup or skip it. A Completed-Change Verification failure that traces to a single refactoring step gets the same treatment: revert that step only, then rerun Completed-Change Verification; one that does not trace to a single step follows its hard gate above. A lint regression (when `lintCommand` is set) gets the same treatment: identify the refactoring step that introduced it, revert that step only, and try a simpler cleanup or skip it — not the hard retry-3x-then-stop gate Phase 4 uses.
