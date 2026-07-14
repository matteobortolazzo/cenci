# Phase 1: Plan

Read this file only when Phase 1 starts.

## Existing Plan

If `hasPlanFile` is true, skip new planning:

1. The plan file was already read and parsed during mode detection. Source ticket details, user context, Q&A, implementation plan, architectural context, design context, and attachment summaries from it.
2. Compare `planCommitSha` from front matter to `git rev-parse HEAD`. If they differ, inspect the intervening commits and their diffs against the plan's scope and assumptions. A SHA mismatch alone is not a reason to ask the user. If the changes are clearly unrelated or do not invalidate or materially complicate the plan, briefly note that the plan is behind but still applicable and continue automatically. Ask only when there is a concrete conflict or reasonable doubt about whether the existing plan remains valid. In that case, explain the specific potentially relevant changes and use `AskUserQuestion` with "Continue with existing plan" and "Re-plan from scratch". If re-planning, delete the plan file and run normal planning. No board-label change is needed here: this is a plan-file-mode run, so `Working` was already added at pipeline start (`Planned` stays — see the **Label "Working"** section of `SKILL.md`); the re-run's new-plan path re-applies (harmlessly re-adds) `Planned` at the end when it persists the fresh plan.
3. In ticket mode, re-fetch the ticket and compare state/body with `## Ticket Details`. If changed, require confirmation via `AskUserQuestion` ("Continue" / "Abort") before continuing. (This single read-only `gh issue view` is the sanctioned exception to the "no ticket fetch in the main agent" rule — it runs after the pre-flight check, and the context-gatherer is not used in plan file mode.)
4. Proceed to Phase 2 with context from the plan file.

## Trivial Fast Path

If `hasPlanFile` is false and the main agent's Trivial-Ticket Triage (see `SKILL.md`) set `trivial = true`, take this branch instead of `## New Plan`:

1. The AC-mandated line (`` Judged trivial: `<reason>` — skipping planning, implementing directly ``) was already printed once by `SKILL.md`'s `## Trivial-Ticket Triage` when it set `trivial = true`. Do **not** print it again here.
2. Skip the planner delegation and the Q&A loop entirely — there are no clarifying questions to ask on this path.
3. Write the plan file using the **same** "Persist the Plan" machinery below, verbatim: the same front-matter shape, the same ticket-title→slug derivation, a `Write` step for the main-agent-owned sections, then `cat /tmp/claude/agentflow-context-<id>.md >> .plans/<filename>` to append `## Ticket Details`, `## Design Context`, and `## Project Context`. Two content differences: `## Implementation Plan` in this minimal file is a one-liner pointing at `## Ticket Details`, e.g. "Trivial ticket — implementation follows the ticket body directly; see ## Ticket Details." Likewise, `## Architectural Context` is a one-liner in place of the planner's discovered patterns/conventions, e.g. "N/A — no codebase exploration; triage judged the ticket unambiguous from its own body."
4. Apply the `Planned` label exactly as `## Persist the Plan`'s "Mark the ticket `Planned`" step does, **except** do not remove `Working` — this session is continuing rather than stopping, so the normal flow's
   ```bash
   gh issue edit <number> --repo <owner>/<repo> --add-label "Planned" --remove-label "Working"
   ```
   becomes just:
   ```bash
   gh issue edit <number> --repo <owner>/<repo> --add-label "Planned"
   ```
   **Verify this command succeeded before continuing.** This restates — does not merely reference — `## Persist the Plan`'s error-surfacing rule, and it is *more* load-bearing here: the normal flow's plan-review stop is itself a human checkpoint that would catch a silently-failed label swap, but the Trivial Fast Path has no such checkpoint — it continues straight into Phase 2 and can arm an unattended `/goal` autopilot all the way to PR creation. If this command errors, surface the error to the user and **STOP** — do not set `hasPlanFile = true`, do not arm the goal, and do not proceed into Phase 2 on an unconfirmed board state.
5. If `agentflow.planComment: true`, post the minimal plan as an audit comment exactly as today (see `## Persist the Plan`).
6. Set `hasPlanFile = true` and continue directly into Phase 2 in the same session. Do **not** stop, do **not** present the plan for review, do **not** end the turn — this is the sole exception to "a session that creates a new plan always ends at Phase 1" (see `SKILL.md`'s Pipeline section).

## New Plan

If `hasPlanFile` is false (and the Trivial Fast Path above did not apply), analyze the codebase, ask clarifying questions, produce a plan, persist it, present it, and stop.

Mandatory stops:

1. If the planner has clarifying questions, ask them with `AskUserQuestion` and end the turn.
2. Once the planner has no remaining questions, persist the plan and stop, presenting the full plan in the final message. There is **no plan-approval prompt**: answering the clarifying questions is the user's input to planning; reviewing the saved plan and launching the plan-file run is the approval. Implementation resumes by invoking `/agentflow:implement .plans/<filename>` in a fresh session.

Never begin Phase 2 in a session that created a new plan — not in the same turn, and not in a later turn. Phases 2–9 require invocation with a plan-file argument, except the Trivial Fast Path (see `## Trivial Fast Path` above).

## Optional Deep Exploration

If `.claude/config.json` has `"deepExploration": true`, launch two Explore-type subagents before planner delegation:

- Explorer 1: feature area, related components/services/patterns. Write full notes to `/tmp/claude/agentflow-explore-1.md`.
- Explorer 2: cross-cutting concerns, shared utilities, middleware, configuration, integrations. Write full notes to `/tmp/claude/agentflow-explore-2.md`.

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
2. Append the context bundle verbatim via shell: `cat /tmp/claude/agentflow-context-<id|slug>.md >> .plans/<filename>` — this contributes `## Ticket Details`, `## Design Context`, and `## Project Context`.

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
stalenessPaths: agentwatch
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
<patterns, conventions, code structures discovered>

## Design Context
<DESIGN.md content or .pen path, or "N/A">

## Project Context
<per-project CLAUDE.md content for affected projects (monorepo only) — section may be absent>

## Attachment Summaries
<summaries or "None">
```

Record `planCommitSha` from `git rev-parse HEAD`. Source `isChild`, `isLastChild`, and `parentId` from the context-gatherer digest stored earlier in this session. Source `stalenessPaths` from the repo-relative project directory/directories the plan touches (e.g. `agentwatch`, or `agentwatch, agentflow` for a plan spanning both), as identified in the planner output / context digest — this lets `agentwatch dispatch` count only commits relevant to this plan when judging staleness, instead of every commit in the monorepo. Omit or leave empty in single-project repos, where whole-repo staleness counting is fine. For ticketless mode, omit ticket fields.

### Mark the ticket `Planned` (ticket mode only)

After the plan file is written, signal on the board that a plan is now waiting to be picked up. **Ticket mode only** — skip this entirely in ticketless mode (there is no ticket to label).

`gh issue edit --add-label` **fails when the label does not exist in the repository** — `Planned` is newer than the other lifecycle labels, so projects configured before it are missing it. Ensure it exists first, as its own Bash call (`|| true` swallows only the "already exists" error):

```bash
gh label create "Planned" --repo <owner>/<repo> --color "1D76DB" --description "Plan on disk, ready to pick up" 2>/dev/null || true
```

Then apply the swap and **verify it succeeded** — if this command errors, surface the error to the user instead of ending the session silently, since a missing `Planned` label breaks the saved-plan pickup on the board:

```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Planned" --remove-label "Working"
```

`Planned` means "a persisted plan exists (or has existed) on disk (`.plans/<id>-*.md`)." The planning session applied `Working` at pipeline start; this swap replaces it so the board no longer shows the ticket as actively in flight. Unlike `Working`, `Planned` is a milestone marker, not a current-stage indicator — the implement skill never removes it once set, including at the start of the plan-file implementation run (see the **Label "Working"** section of `SKILL.md`), so a stalled implementation run still shows `Planned` on the board.

If `.claude/config.json` has `agentflow.planComment: true`, also post the saved plan as a ticket comment for audit / off-host visibility (ticket mode only), immediately after the label swap. `.plans/` remains the executable source of truth; the comment is a convenience copy:

```bash
gh issue comment <number> --repo <owner>/<repo> --body-file .plans/<filename>
```

If `agentflow.planComment` is absent or `false`, skip the comment.

After the plan file is written and (in ticket mode) the label swap and any optional comment are done, the **only remaining actions** are those one-or-two `gh` calls plus the final message below — no other tool calls, and never read `phases/phase-2-worktree.md` or any later phase file in this session. The session that created a plan always ends here; implementation runs in a fresh session. (This "always ends here" rule is what `## New Plan` follows; the **Trivial Fast Path** above reuses this section's front-matter/write/label/comment machinery but does not stop — see its step 6.)

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

/agentflow:implement .plans/<filename>

To discard it and re-plan, re-run /agentflow:implement <ticket-id or task> with `replan` as context.

If the task risks exceeding the implementing agent's context budget (see `docs/ticket-sizing.md`), consider running /refine to split it first.

The SessionStart hook will also remind you of pending plans.
```

Launching the plan-file run is the human gate for the autopilot: saving the plan arms nothing — the plan-file run the user launches after reviewing it arms a `/goal` completion condition (Claude Code ≥ 2.1.139) so phases 2–9 resume through to an open PR instead of stalling on a mid-phase stop. Do **not** set any goal in this (`## New Plan`) session — it ends here, and goals are session-scoped. The Trivial Fast Path is the exception: it does not end here, and arms the goal itself once it reaches Phase 2 (see `SKILL.md`'s Goal Autopilot section).
