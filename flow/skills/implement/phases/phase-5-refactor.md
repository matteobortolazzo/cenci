# Phase 5: Refactor

Read this file only when Phase 5 starts. Skip this separate phase if Phase 3 is running approved compact implementation mode — with one carve-out: the `## Reuse Check` below still runs, folded into that single implementer delegation. Compact mode reaches it by reading that one section directly: Phase 3's `## Compact Implementation` checklist sends the implementer here for it, so the check's steps and cost guards are defined in exactly one place and neither path runs it from memory. It is a handful of searches over code the agent is already holding, so skipping it saves nothing worth the gap it opens.

Delegate to the `implementer` agent for focused cleanup of touched code only.

## Process

Pass:

- Worktree path. Tell the agent: target the worktree explicitly on every command — via `git -C <worktree-path>` for git commands, absolute paths for file operations, or the client's working-directory option — do **not** prefix every command with `cd <path> &&`. See the `shell-rules` skill for command patterns.
- Changed file list.
- LSP diagnostic reminder if configured.
- The project's `lintCommand` (when set).
- Resolved topic docs: at most 3 `docs/<topic>.md` paths matched to Files to Modify/Create.

Review changed code for:

- Dead code or unnecessary abstractions.
- Duplicated logic; consolidate only when used 3+ times or clearly established locally.
- Unclear names.
- Complex conditionals that can be simplified.
- Overly clever code.

That duplication bullet governs duplication that was *already there* and this change merely touched. Duplication this change **introduces** is governed by `## Reuse Check` below — run it as part of the same delegation, not a separate pass.

Run the full test suite once all cleanup is done — including the `## Reuse Check` below — and rerun lint (when `lintCommand` is set) alongside it. An absent `lintCommand` skips the lint step cleanly — no error. Behavior must not change.

## Reuse Check

Everything above scopes to touched code, which leaves one blind spot: a helper, constant, or fixture added by this change that already exists elsewhere in the repo looks perfectly clean from inside the diff. Nothing in the pipeline sees it — Phase 4 only cares that tests pass, and the Phase 6 + 7 reviewers read the same diff. Close that specific gap here, cheaply. This is a targeted check on new code, **never a repo-wide duplication sweep** — that is `/cenci:refactor`'s job, it fans out dedicated analyzers over a whole scope and emits tickets rather than fixes, and pulling it into every ticket's pipeline is not the trade this step makes.

Tell the implementer to run these steps inside the same delegation:

1. List the **named units this diff adds** — new functions, methods, helpers, exported constants, test fixtures, and setup helpers. Additions only: an edit or rename of an existing unit does not count.
2. **Skip the rest of this check entirely when that list is empty.** Pure edits, deletions, and config/data-only diffs are the common case and must cost nothing.
3. For at most the 10 largest added units (by body size), search for an existing equivalent on a behavior-bearing name fragment *and* on a distinctive line from the body — a re-implementation rarely reuses the exact name, so a name-only search is the one that misses. That is two searches per unit, so **at most 20 searches for the whole check**. Restrict the search to the affected project's directory rather than the whole tree; in a monorepo an equivalent in a sibling project is usually not reusable anyway. Derive that directory from the changed file list this phase already receives, not from any earlier phase's state — Phase 2's baseline gate resolves `projects[].slug` values, not paths, and it is skipped entirely on configs with no `gateCommand` and no `projects[]`, so nothing upstream is guaranteed to hand a path down. Take the longest `projects[].path` in the resolved config that prefixes the changed files; when the config declares no `projects[]`, or the changed files sit under no declared path, fall back to the deepest single directory that contains all of them. Never fall back to the whole tree.
4. On a hit, prefer the existing unit: call it, or widen it when the new use needs one more parameter or one more case. Consolidate here even at **two** occurrences — the rule-of-three threshold above exists because rewriting settled code is risky, and that reasoning does not apply when the second occurrence is the one being written right now and is free to simply not exist.
5. When the existing equivalent cannot be reused **without changing behavior for its current callers**, keep the new code and report the near-duplicate in one line in this phase's summary, opened with the literal prefix `Reuse Check:` so Phase 6 + 7 can carry it forward verbatim into `$RUN_DIR/reuse-notes.txt` (see that phase's `## Shared Context`) and Phase 9 can render it without re-deriving anything. Do not rewire the existing unit's other callers to make it fit — that is outside the ticket's scope. A near-duplicate left in place is a refactor/tech-debt observation, so it lands under Phase 9's `### Considered and discarded` and is **never** tracked or turned into a Followup ticket — the same policy the Phase 6 + 7 reviewers already apply to their own refactor findings.

Any consolidation made here is part of this phase's changes: the same full-suite-and-lint run above covers it, and the same Error Recovery below applies to it.

## Error Recovery

If tests fail, identify the specific refactoring step that broke behavior, revert that step only, and try a simpler cleanup or skip it. A lint regression (when `lintCommand` is set) gets the same treatment: identify the refactoring step that introduced it, revert that step only, and try a simpler cleanup or skip it — not the hard retry-3x-then-stop gate Phase 4 uses.
