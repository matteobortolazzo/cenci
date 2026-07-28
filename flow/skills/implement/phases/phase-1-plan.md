# Phase 1: Plan

Read this file only when Phase 1 starts.

## Pipeline: Plan Stage

Ticket mode only, regardless of which branch below runs: at planning start, invoke `cenci pipeline plan <id>` to record `waiting_for_plan_approval` and obtain this stage's `next_actions`/`warnings`/`errors`. Render this as the one-line status update in place of the phase-transition prose each branch below used to narrate on its own. If it returns non-empty `errors[]` (e.g. `plan` invoked before `prepare`), surface them and stop before continuing into any branch below. `plan` here does not itself gate Phase 2 — approval is recorded separately when the human launches the plan-file run (see `phase-2-worktree.md`'s Gate Check). Ticketless mode: skip this invocation — the pipeline commands operate on ticket IDs.

## Existing Plan

If `hasPlanFile` is true, skip new planning:

1. The plan file was already read during Pre-flight Check's **Plan Verification** (see `SKILL.md`). Source ticket details, user context, Q&A, implementation plan, architectural context, design context, and attachment summaries from it.
2. Render the `cenci pipeline plan-check <id>` verdict stored as `planCheckDecision` during Plan Verification: `resume` → continue directly to step 3 below, nothing further to confirm. `stale` → do **not** ask blindly. First analyze *why* the plan is stale and form a recommendation, then use `AskUserQuestion` with "Continue with existing plan" and "Re-plan from scratch", ordering the option you recommend **first** and appending `(Recommended)` to its label. The deterministic CLI verdict is never overridden — your analysis only chooses the *recommended default*; the human still decides. This decision has two distinct sources, so pick the matching analysis:
   - The common case: the CLI determined the plan is behind (commits-behind on `stalenessPaths` since `planCommitSha`, the ticket closed, or the ticket updated after the plan's `createdAt`). Run `git log --stat <planCommitSha>..HEAD -- <stalenessPaths>` (both values from the plan's front matter; split the comma-separated `stalenessPaths` into separate space-separated pathspecs for git) to list exactly which commits touched the files this plan depends on, read those diffs against the plan's `## Implementation Plan`, and judge whether they invalidate it. Present that judgment as the recommendation — e.g. "2 commits landed on your plan's files but only touch unrelated logging → recommend Continue" vs "commit `abc123` reworked `CheckPlan`, which your plan modifies → recommend Re-plan". If `git log` shows **no** commits (the `stale` verdict came from the ticket closing or being edited after `createdAt`, not repo churn), say so and recommend Re-plan by default, since the ticket change cannot be diffed from here.
   - The `multiple`-disambiguation case (see `SKILL.md`'s Plan Verification `multiple` bullet): `cenci pipeline plan-check` never computed a freshness verdict for a file picked by hand from several candidates, so there is no commits-behind signal to analyze — do not run the `git log` analysis. Explain that freshness could not be automatically verified for this file (not that it is known to be behind), before asking the same "Continue with existing plan" / "Re-plan from scratch" question.

   If re-planning, delete the plan file and run normal planning. No board-label change is needed here: this is a plan-file-mode run, so `Working` was already added at pipeline start (`Planned` stays — see the **Label "Working"** section of `SKILL.md`); the re-run's new-plan path re-applies (harmlessly re-adds) `Planned` at the end when it persists the fresh plan.
3. Render the `next_actions` from the `cenci pipeline plan <id>` call above (see `## Pipeline: Plan Stage`) as this step's status update; they point to Phase 2.

## Trivial Fast Path

If `hasPlanFile` is false and the main agent's Trivial-Ticket Triage (see `SKILL.md`) set `trivial = true`, take this branch instead of `## New Plan`:

1. The AC-mandated line (`` Judged trivial: `<reason>` — skipping planning, implementing directly ``) was already printed once by `SKILL.md`'s `## Trivial-Ticket Triage` when it set `trivial = true`. Do **not** print it again here.
2. Skip the planner delegation and the Q&A loop entirely — there are no clarifying questions to ask on this path.
3. Write the plan file using the **same** "Persist the Plan" machinery below, verbatim: the same front-matter shape, the same ticket-title→slug derivation, a `Write` step for the main-agent-owned sections, then `cat ${TMPDIR:-/tmp}/cenci/cenci-context-<id>.md >> .plans/<filename>` to append `## Ticket Details`, `## Design Context`, and `## Project Context`. Two content differences: `## Implementation Plan` in this minimal file is a one-liner pointing at `## Ticket Details`, e.g. "Trivial ticket — implementation follows the ticket body directly; see ## Ticket Details." Likewise, `## Architectural Context` is a one-liner in place of the planner's discovered patterns/conventions, e.g. "N/A — no codebase exploration; triage judged the ticket unambiguous from its own body." Then run assembly step 3's four-heading verification exactly as written (see `## Persist the Plan`, step 3), including its `## Design Context` self-repair and its hard stop — on this path the hard stop additionally means not running step 5's `--trivial` label call, not recording the artifact in step 6, not setting `hasPlanFile = true`, not arming the goal, and not entering Phase 2.
4. If `cenci.planComment: true`, post the minimal plan as an audit comment exactly as today (see `## Persist the Plan`). As there, the comment must come **before** the label call in the next step — the label call records the ticket's post-edit `updatedAt` as the plan-freshness baseline, so it must be the last call that edits the ticket.
5. Apply the `Planned` label exactly as `## Persist the Plan`'s "Mark the ticket `Planned`" step does, **except** retain `Working` instead of swapping it out — this session is continuing rather than stopping, so the normal flow's
   ```bash
   cenci pipeline label <id> --transition planned
   ```
   becomes:
   ```bash
   cenci pipeline label <id> --transition planned --trivial
   ```
   The `--trivial` flag tells the CLI to keep `Working` (add `Planned` alongside it) instead of swapping `Working` out.

   **Verify this call succeeded before continuing** — render its `state`/`next_actions`/`warnings`/`errors`. This restates — does not merely reference — `## Persist the Plan`'s error-surfacing rule, and it is *more* load-bearing here: the normal flow's plan-review stop is itself a human checkpoint that would catch a silently-failed label swap, but the Trivial Fast Path has no such checkpoint — it continues straight into Phase 2 and can arm an unattended `/goal` autopilot all the way to PR creation. If this call returns non-empty `errors[]`, surface them to the user and **STOP** — do not set `hasPlanFile = true`, do not arm the goal, and do not proceed into Phase 2 on an unconfirmed board state.
6. Record the saved plan path as a tracked artifact:
   ```bash
   cenci pipeline artifact <id> --plan .plans/<filename>
   ```
7. Set `hasPlanFile = true` and continue into Phase 2 in the same session, per the `next_actions` rendered from the `cenci pipeline plan <id>` call above (see `## Pipeline: Plan Stage`). Do **not** stop, do **not** present the plan for review, do **not** end the turn — this is the sole exception to "a session that creates a new plan always ends at Phase 1" (see `SKILL.md`'s Pipeline section).

## New Plan

If `hasPlanFile` is false (and the Trivial Fast Path above did not apply), analyze the codebase, ask clarifying questions, produce a plan, persist it, present it, and stop.

Mandatory stops:

1. If the planner has clarifying questions, ask them with `AskUserQuestion` and end the turn.
2. Once the planner has no remaining questions, persist the plan and stop, presenting the full plan together with the `next_actions` rendered from the `cenci pipeline plan <id>` call above (see `## Pipeline: Plan Stage`) in the final message. There is **no plan-approval prompt**: answering the clarifying questions is the user's input to planning; reviewing the saved plan and launching the plan-file run is the approval — which is what advances the pipeline state past `waiting_for_plan_approval` (see `phase-2-worktree.md`'s Gate Check). Implementation resumes by invoking `/cenci:implement .plans/<filename>` in a fresh session.

Never begin Phase 2 in a session that created a new plan — not in the same turn, and not in a later turn. Phases 2–9 require invocation with a plan-file argument, except the Trivial Fast Path (see `## Trivial Fast Path` above).

## Optional Deep Exploration

If the resolved config has `"deepExploration": true`, launch two Explore-type subagents before planner delegation:

- Explorer 1: feature area, related components/services/patterns. Write full notes to `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-explore-1.md`.
- Explorer 2: cross-cutting concerns, shared utilities, middleware, configuration, integrations. Write full notes to `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-explore-2.md`.

Each explorer must write its detailed findings to its notes file and return only a summary of 10 lines or fewer. Pass the two file paths (not the notes content) to the planner, which reads them itself. If `deepExploration` is absent or false, skip this.

## Planner Delegation

Delegate to the `planner` agent. The main agent owns all user interaction.

Pass context **by path, not by paste** — the planner has `Read` and must read the bundle itself as its first step.

For ticket mode, pass:

- The context bundle path (from the context-gatherer digest) — contains ticket details, design context, and project context.
- The digest's summary bullets.
- User context from `$ARGUMENTS`, if present.
- A requirement for a Design Mapping section when the bundle contains design context.
- Explorer notes file paths, if deep exploration ran.
- Attachment paths; for UI mockups, require the plan to match the visual design.

For ticketless mode, pass:

- The task description as the primary spec.
- The context bundle path, if a gatherer ran (design/monorepo context).
- Explorer notes file paths, if deep exploration ran.
- A note that there are no formal acceptance criteria, so scope must be derived and ambiguities clarified.

In both modes, tell the planner:

- Read project `CLAUDE.md` and `README.md` when relevant.
- Read relevant `docs/<topic>.md` files on demand, not all docs.
- Read legacy `.claude/rules/lessons-learned*.md` only if present.
- Ask at most 6 clarifying questions, only where answers would change the plan.

Question categories to evaluate: scope boundaries, edge cases, error handling, performance, backward compatibility, and integration points.

## Route Planner Output

Parse `## Clarifying Questions`.

- If questions exist and are not "None", present all questions using the planner's wording via `AskUserQuestion`; end the turn. When the answers arrive, re-invoke the planner with the bundle path and the Q&A pairs — do not re-paste ticket or design content — and route its output through this section again.
- If no questions (or none remain), persist the plan directly — do **not** ask for approval. The human gate is launching the plan-file run: the user reviews the saved plan in the final message and either launches implementation or re-plans (`replan` as user context discards the saved plan).

## Persist the Plan

Once the planner has no remaining questions, create `.plans/` and assemble:

- Ticket mode: `.plans/<ticket-id>-<slug>.md`
- Ticketless mode: `.plans/<slug>.md`

**Assemble, don't re-emit.** Write the plan file in two steps so bundle content never passes through the main context again:

1. `Write` the plan file with the YAML front matter and only the sections the main agent owns: `## User Context`, `## Q&A from Planning`, `## Implementation Plan`, `## Architectural Context`, `## Attachment Summaries`.
2. Append the context bundle verbatim via shell: `cat ${TMPDIR:-/tmp}/cenci/cenci-context-<id|slug>.md >> .plans/<filename>` — this contributes `## Ticket Details`, `## Design Context`, and `## Project Context`.
3. **Verify the assembled plan.** Before anything in the `Mark the ticket `Planned`` step below runs, confirm the assembled file actually contains every heading `cenci pipeline plan-check` requires:

   ```bash
   for s in "## Ticket Details" "## Implementation Plan" "## Architectural Context" "## Design Context"; do
     grep -q -- "^${s}" ".plans/<filename>" || echo "MISSING: ${s}"
   done
   ```

   Those four literals are the source-of-truth `requiredPlanSections` values in `watch/internal/pipeline/planfile.go:49-55`; a plan missing any of them fails `plan-check` with `ErrPlanMalformed` on the next `/cenci:implement` run. Any future change to that validator's list MUST update this step in the same PR. The validator matches each heading as a plain substring while this check is line-anchored, so passing here is strictly sufficient for the validator, never the reverse.

   - **Nothing missing** → continue to `Mark the ticket `Planned``.
   - **`## Design Context` missing and nothing else** → self-repair: append the two lines `## Design Context` and `N/A` to the end of the plan file, re-run the four-heading check above to confirm it now passes, then continue. Appending at the end is correct — consumers locate sections by heading, so order is not significant (see "Section order in the assembled file…" below). This repair is never silent: report it in this phase's final message, e.g. "context bundle omitted `## Design Context`; self-repaired with `N/A` — gatherer non-compliance, see #610". If the re-check still reports `## Design Context` missing after the append (e.g. disk full, wrong filename interpolation), stop here exactly as the hard-stop branch below does — no further self-repair attempt and none of that branch's forbidden actions — and report the append failure to the user instead of the usual repair notice.
   - **`## Ticket Details`, `## Implementation Plan`, or `## Architectural Context` missing** (alone or in any combination, and regardless of whether `## Design Context` is missing too) → **hard stop**. These cannot be safely defaulted: `## Ticket Details` comes from the context bundle appended in step 2, and `## Implementation Plan` / `## Architectural Context` come from the planner output written in step 1, so a missing one means the assembly itself lost content. Do **not** self-repair, do **not** post the `planComment` audit comment, do **not** run the `Planned` label transition, do **not** record the plan artifact, and do **not** set `hasPlanFile = true`. Report which heading(s) are missing and which assembly input is implicated (context bundle vs. planner sections), then stop so the user can re-run the gatherer/planner. Leave the malformed plan file on disk for inspection — `.plans/` is gitignored state.

   This step runs before the `planComment` comment so a malformed plan body is never posted to the ticket, and before the label transition so the `updatedAt` freshness baseline is never recorded for a plan that would fail validation.

If no bundle exists (ticketless mode without a gatherer run), write `## Ticket Details` with the task description and `## Design Context` with "N/A" directly in step 1.

Section order in the assembled file differs from the template below; consumers locate sections by heading, so order is not significant.

Use YAML front matter:

```markdown
---
version: 1
mode: ticket | ticketless
ticketId: 42
ticketTitle: "Add dark mode support"
slug: add-dark-mode
isChild: false
isLastChild: false
parentId: null
createdAt: 2026-03-04T10:30:00Z
status: planned
planCommitSha: abc123def
stalenessPaths: watch/internal/pipeline/planfile.go, flow/skills/implement/phases/phase-1-plan.md
---

## Ticket Details
<verbatim ticket body or task description>

## User Context
<additional user context or "None">

## Q&A from Planning
<numbered Q&A pairs or "No questions asked">

## Implementation Plan
<planner output>

## Architectural Context
<the planner output's ## Architectural Context section — patterns, conventions, integration points discovered>

## Design Context
<DESIGN.md content or .pen path, or "N/A">

## Project Context
<per-project CLAUDE.md content for affected projects (monorepo only) — section may be absent>

## Attachment Summaries
<summaries or "None">
```

Record `planCommitSha` from `git rev-parse HEAD`. Source `isChild`, `isLastChild`, and `parentId` from the context-gatherer digest stored earlier in this session. Source `stalenessPaths` from the plan's **dependency file set** — the exact repo-relative files under `### Files to Modify` and `### Files to Create` in the planner output, plus any files the planner explicitly flags as contracts/interfaces this plan reads and relies on (not just the ones it edits). List the files, comma-separated, not the enclosing project directory: `cenci pipeline plan-check` and `cenci dispatch` both scope their commits-behind count with `git rev-list -- <stalenessPaths>`, which counts commits touching those exact paths, so a directory value (e.g. `cenci`) marks the plan stale on *every* commit anywhere in that project — in a busy repo that fires on unrelated churn and the plan is perpetually "stale". File-level paths count only commits that actually touch what this plan depends on. This applies in single-project repos too: prefer the plan's file set over a bare repo-root/whole-repo value there as well, for the same reason. Only fall back to a directory (or omitting `stalenessPaths` entirely) when the plan genuinely cannot enumerate its files. For ticketless mode, omit ticket fields.

### Mark the ticket `Planned` (ticket mode only)

After the plan file is written, signal on the board that a plan is now waiting to be picked up, and record the plan path as a tracked artifact. **Ticket mode only** — skip this entirely in ticketless mode (there is no ticket to label; the artifact call below is also skipped since it's keyed by ticket ID).

First, if the resolved config has `cenci.planComment: true`, post the saved plan as a ticket comment for audit / off-host visibility (ticket mode only). `.plans/` remains the executable source of truth; the comment is a convenience copy:

```bash
gh issue comment <number> --repo <owner>/<repo> --body-file .plans/<filename>
```

If `cenci.planComment` is absent or `false`, skip the comment. The comment must come **before** the label swap below: the swap records the ticket's post-edit `updatedAt` as the plan-freshness baseline that `cenci pipeline plan-check` compares against on the saved-plan pickup, so the label swap has to be the **last** call that edits the ticket — a comment posted after it would re-bump `updatedAt` past the recorded baseline and mark the just-persisted plan stale.

Then apply the swap and **verify it succeeded** — if this call returns non-empty `errors[]`, surface it to the user instead of ending the session silently, since a missing `Planned` label breaks the saved-plan pickup on the board:

```bash
cenci pipeline label <id> --transition planned
```

Render the returned `state`/`next_actions`/`warnings`/`errors`. The CLI self-heals `Planned`'s existence in the repository (it is newer than the other lifecycle labels, so projects configured before it may be missing it) and treats "already exists" as success — no separate self-heal call is needed.

`Planned` means "a persisted plan exists (or has existed) on disk (`.plans/<id>-*.md`)." The planning session applied `Working` at pipeline start; this call replaces it so the board no longer shows the ticket as actively in flight. Unlike `Working`, `Planned` is a milestone marker, not a current-stage indicator — the implement skill never removes it once set, including at the start of the plan-file implementation run (see the **Label "Working"** section of `SKILL.md`), so a stalled implementation run still shows `Planned` on the board.

Record the saved plan path as a tracked artifact:

```bash
cenci pipeline artifact <id> --plan .plans/<filename>
```

After the plan file is written and (in ticket mode) any optional comment, the label transition, and artifact recording are done, the **only remaining actions** are those `cenci pipeline`/`gh` calls plus the final message below — no other tool calls, and never read `phases/phase-2-worktree.md` or any later phase file in this session. The session that created a plan always ends here; implementation runs in a fresh session. (This "always ends here" rule is what `## New Plan` follows; the **Trivial Fast Path** above reuses this section's front-matter/write/comment/label/artifact machinery but does not stop — see its step 7.)

Stop and present the full plan together with the save notice — this final message is the user's review point before launching implementation, so never abbreviate the plan here:

```text
## Implementation Plan

<planner's full plan>

### Assumptions
<assumptions>

### Open Questions
<unresolved items or "None">

### Risks
<risks>

---

Plan saved to `.plans/<filename>`.

Review the plan above. To implement, start a fresh session and run:

/cenci:implement .plans/<filename>

To discard it and re-plan, re-run /cenci:implement <ticket-id or task> with `replan` as context.

If the task risks exceeding the implementing agent's context budget (see `docs/ticket-sizing.md`), consider running /cenci:refine to split it first.

The SessionStart hook will also remind you of pending plans.
```

Launching the plan-file run is the human gate for the autopilot: saving the plan arms nothing — the plan-file run the user launches after reviewing it attempts to arm a `/goal` completion condition so phases 2–9 resume through to an open PR instead of stalling on a mid-phase stop. Do **not** set any goal in this (`## New Plan`) session — it ends here, and goals are session-scoped. The Trivial Fast Path is the exception: it does not end here, and arms the goal itself once it reaches Phase 2 (see `SKILL.md`'s Goal Autopilot section).
