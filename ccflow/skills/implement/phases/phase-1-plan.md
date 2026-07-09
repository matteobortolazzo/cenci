# Phase 1: Plan

Read this file only when Phase 1 starts.

## Existing Plan

If `hasPlanFile` is true, skip new planning:

1. The plan file was already read and parsed during mode detection. Source ticket details, user context, Q&A, implementation plan, architectural context, design context, and attachment summaries from it.
2. Compare `planCommitSha` from front matter to `git rev-parse HEAD`. If they differ, warn: "The codebase has changed since this plan was created (`planCommitSha` vs current HEAD). The plan may be stale. Continue anyway?" Use `AskUserQuestion` with "Continue with existing plan" and "Re-plan from scratch". If re-planning, delete the plan file and run normal planning. No board-label removal is needed here: this is a plan-file-mode run, so the `Planned → Working` swap already happened at pipeline start (see the **Label "Working"** section of `SKILL.md`); the re-run's new-plan path re-applies `Planned` at the end when it persists the fresh approved plan.
3. In ticket mode, re-fetch the ticket and compare state/body with `## Ticket Details`. If changed, warn the user and require confirmation before continuing. (This single read-only `gh issue view` is the sanctioned exception to the "no ticket fetch in the main agent" rule — it runs after the pre-flight check, and the context-gatherer is not used in plan file mode.)
4. Proceed to Phase 2 with context from the plan file.

## New Plan

If `hasPlanFile` is false, analyze the codebase, ask clarifying questions, produce a plan, get approval, persist it, and stop.

Mandatory stops:

1. If the planner has clarifying questions, ask them with `AskUserQuestion` and end the turn.
2. Present every plan for approval with `AskUserQuestion` and end the turn.
3. After approval, persist the plan and stop. Implementation resumes by invoking `/ccflow:implement .plans/<filename>` in a fresh session.

Never begin Phase 2 in a session that created a new plan — not in the same turn, and not in a later turn. Phases 2–9 require invocation with a plan-file argument.

## Optional Deep Exploration

If `.claude/config.json` has `"deepExploration": true`, launch two Explore-type subagents before planner delegation:

- Explorer 1: feature area, related components/services/patterns. Write full notes to `/tmp/claude/ccflow-explore-1.md`.
- Explorer 2: cross-cutting concerns, shared utilities, middleware, configuration, integrations. Write full notes to `/tmp/claude/ccflow-explore-2.md`.

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

- If questions exist and are not "None", present all questions using the planner's wording via `AskUserQuestion`; end the turn.
- If no questions, present the plan for approval.
- If the user requests changes, ask what needs changing, re-invoke planner with the bundle path, the Q&A pairs, and the change request — do not re-paste ticket or design content — then repeat approval.

## Approval Output

Present:

```markdown
## Implementation Plan

<planner's full plan>

### Assumptions
<assumptions>

### Open Questions
<unresolved items or "None">

### Risks
<risks>

If the task appears too large for a single PR, consider running `/refine` to split it first.
```

Then call `AskUserQuestion` with Approve / Request Changes. Do not call any other tool after this question.

## Persist Approved Plan

After approval, create `.plans/` and assemble:

- Ticket mode: `.plans/<ticket-id>-<slug>.md`
- Ticketless mode: `.plans/<slug>.md`

**Assemble, don't re-emit.** Write the plan file in two steps so bundle content never passes through the main context again:

1. `Write` the plan file with the YAML front matter and only the sections the main agent owns: `## User Context`, `## Q&A from Planning`, `## Implementation Plan`, `## Architectural Context`, `## Attachment Summaries`.
2. Append the context bundle verbatim via shell: `cat /tmp/claude/ccflow-context-<id|slug>.md >> .plans/<filename>` — this contributes `## Ticket Details`, `## Design Context`, and `## Project Context`.

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
status: approved
planCommitSha: abc123def
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

Record `planCommitSha` from `git rev-parse HEAD`. Source `isChild`, `isLastChild`, and `parentId` from the context-gatherer digest stored earlier in this session. For ticketless mode, omit ticket fields.

### Mark the ticket `Planned` (ticket mode only)

After the plan file is written, signal on the board that an approved plan is now waiting to be picked up. **Ticket mode only** — skip this entirely in ticketless mode (there is no ticket to label):

```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Planned" --remove-label "Working"
```

`Planned` means "an approved plan exists on disk (`.plans/<id>-*.md`), ready to pick up." The planning session applied `Working` at pipeline start; this swap replaces it so the board no longer shows the ticket as actively in flight. The `Planned → Working` swap happens later, at the start of the plan-file implementation run (see the **Label "Working"** section of `SKILL.md`).

If `.claude/config.json` has `ccflow.planComment: true`, also post the approved plan as a ticket comment for audit / off-host visibility (ticket mode only), immediately after the label swap. `.plans/` remains the executable source of truth; the comment is a convenience copy:

```bash
gh issue comment <number> --repo <owner>/<repo> --body-file .plans/<filename>
```

If `ccflow.planComment` is absent or `false`, skip the comment.

After the plan file is written and (in ticket mode) the label swap and any optional comment are done, the **only remaining actions** are those one-or-two `gh` calls plus the final message below — no other tool calls, and never read `phases/phase-2-worktree.md` or any later phase file in this session. The session that created a plan always ends here; implementation runs in a fresh session.

Stop and tell the user:

```text
Plan saved to `.plans/<filename>`.

To implement, start a fresh session and run:

/ccflow:implement .plans/<filename>

The SessionStart hook will also remind you of pending plans.
```

Approving this plan is the human gate for the autopilot: the plan-file run you launch next arms a `/goal` completion condition (Claude Code ≥ 2.1.139) so phases 2–9 resume through to an open PR instead of stalling on a mid-phase stop. Do **not** set any goal in this planning session — it ends here, and goals are session-scoped. See the **Goal Autopilot** section of `SKILL.md`.
