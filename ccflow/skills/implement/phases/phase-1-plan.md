# Phase 1: Plan

Read this file only when Phase 1 starts.

## Existing Plan

If `hasPlanFile` is true, skip new planning:

1. The plan file was already read and parsed during mode detection. Source ticket details, user context, Q&A, implementation plan, architectural context, design context, and attachment summaries from it.
2. Compare `planCommitSha` from front matter to `git rev-parse HEAD`. If they differ, warn: "The codebase has changed since this plan was created (`planCommitSha` vs current HEAD). The plan may be stale. Continue anyway?" Use `AskUserQuestion` with "Continue with existing plan" and "Re-plan from scratch". If re-planning, delete the plan file and run normal planning.
3. In ticket mode, re-fetch the ticket and compare state/body with `## Ticket Details`. If changed, warn the user and require confirmation before continuing.
4. Proceed to Phase 2 with context from the plan file.

## New Plan

If `hasPlanFile` is false, analyze the codebase, ask clarifying questions, produce a plan, get approval, persist it, and stop.

Mandatory stops:

1. If the planner has clarifying questions, ask them with `AskUserQuestion` and end the turn.
2. Present every plan for approval with `AskUserQuestion` and end the turn.
3. After approval, persist the plan and stop. Implementation resumes by invoking `/ccflow:implement .plans/<filename>` in a fresh session.

Never begin Phase 2 in the same turn as a planning question or plan approval.

## Optional Deep Exploration

If `.claude/config.json` has `"deepExploration": true`, launch two Explore-type subagents before planner delegation:

- Explorer 1: feature area, related components/services/patterns.
- Explorer 2: cross-cutting concerns, shared utilities, middleware, configuration, integrations.

Feed both summaries into the planner. If `deepExploration` is absent or false, skip this.

## Planner Delegation

Delegate to the `planner` agent. The main agent owns all user interaction.

For ticket mode, pass:

- Full ticket details.
- User context from `$ARGUMENTS`, if present.
- Design spec content if loaded; require a Design Mapping section when relevant.
- Design reference path if the ticket references a `.pen` file.
- Attachment paths; for UI mockups, require the plan to match the visual design.

For ticketless mode, pass:

- The task description as the primary spec.
- Design spec content if loaded.
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
- If the user requests changes, ask what needs changing, re-invoke planner with original context plus Q&A and change request, then repeat approval.

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

After approval, create `.plans/` and write:

- Ticket mode: `.plans/<ticket-id>-<slug>.md`
- Ticketless mode: `.plans/<slug>.md`

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

## Attachment Summaries
<summaries or "None">
```

Record `planCommitSha` from `git rev-parse HEAD`. For ticketless mode, omit ticket fields.

Stop and tell the user:

```text
Plan saved to `.plans/<filename>`.

To implement, start a fresh session and run:

/ccflow:implement .plans/<filename>

The SessionStart hook will also remind you of pending plans.
```
