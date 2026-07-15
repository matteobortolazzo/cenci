# Ticket Sizing

## The real constraint: context budget, not line count

The thing that actually limits how big a ticket can be is the **implementing agent's context budget** — roughly **200k tokens** of main-agent context for the whole implementation session (reading the ticket, exploring the codebase, writing code and tests, running builds/tests, reviewing, iterating on feedback). This number is fixed by the model's context window; it is **not configurable** by this project, a ticket, or a user preference.

This is a different axis from PR size. `docs/git-workflow.md` states there is **no hard PR size limit**, and that stands — a PR can be as large as the change genuinely requires. This doc does not contradict that. The ~200k budget is a constraint on what one agent can *do* in one implementation pass, not a line-count cap on the diff it produces. A ticket can still be "too big" even if its resulting diff would be small, and a ticket with a large diff (e.g. a mechanical rename) can be well within budget.

## Why this is estimated, not measured

Neither `/cenci:refine` nor the `planner` agent do deep codebase exploration before producing a size estimate — refine works from the ticket text and user answers, and planner's exploration is bounded (see `docs/git-workflow.md` for PR conventions, and the planner's own "Before Planning" exploration limits). Neither can compute an actual token count for an implementation that hasn't happened yet. So sizing is a **qualitative estimate** based on structural signals visible in the refined ticket, not a numeric per-file or per-line token formula.

## Structural signals

Estimate budget risk from two signals present in a refined ticket's Technical Notes:

1. **Breadth** — the count of distinct affected files/components (services, modules, screens, packages) the ticket names.
2. **Concern spread** — the count of independent concerns named (e.g. API, database, UI, tests, infra/config) — each concern typically requires its own read-explore-implement-verify pass.

## Tiers (qualitative, not a formula)

| Tier | Signal | Budget risk |
|---|---|---|
| Comfortably in budget | 1-2 files/components, a single concern (or two tightly coupled concerns, e.g. one API endpoint plus its test) | Low — one agent can read, implement, and verify well within 200k |
| Moderate | A handful of files within one component/area, or 2-3 related concerns (e.g. API + DB migration for one feature) | Low-to-moderate — still normally fits, but worth a second look if the ticket also has unusually deep exploration needs (large unfamiliar codebase area, complex existing logic to reconcile) |
| Budget risk | Many files spanning multiple independent concerns (e.g. API + DB + UI + tests across unrelated components, or several unrelated components each needing their own exploration) | High — real risk of exhausting the 200k budget partway through implementation, review, or fix-up iteration |

These tiers are judgment calls, not thresholds to count against. Treat "many files spanning multiple independent concerns" as the trigger, not any single number of files or concerns in isolation.

If a ticket could plausibly land in either Moderate or Budget risk, don't resolve the ambiguity silently in favor of not splitting — classify it as Budget risk and say so explicitly in the Size Estimate reasoning (e.g. "L — borderline with Moderate, erring toward split because <reason>"). A close call that gets silently rounded down to Moderate/M loses the one signal that would have let a human catch it before the implementing agent runs out of budget mid-task.

## When to split

Split a ticket **only when there is a real risk of exceeding the ~200k budget** — i.e., the ticket lands in the "Budget risk" tier above.

Do **not** split on any of these grounds alone:
- The ticket touches multiple independent concerns, but each is small (e.g. a small API change plus a small UI change plus its test) — multiple small independent concerns still comfortably fit one implementation pass.
- Splitting would be "cleaner" or more theoretically parallelizable — theoretical parallelism is not a budget concern, and splitting has its own coordination cost (dependency tracking, multiple PRs, multiple review cycles).
- The ticket could be organized into logical commits — that's what multi-commit PRs are for (see `docs/git-workflow.md`); it doesn't require separate tickets.

Splitting is a tool for managing context budget risk, not a tool for organizing work into smaller pieces for its own sake.

## Mapping to S/M/L

- **S** — comfortably in budget. One ticket, one PR, no split, even if it touches a couple of small independent concerns.
- **M** — moderate. One ticket, one PR, no split. Note any exploration-depth concerns in the reasoning, but do not split on concern count alone.
- **L** — budget risk. Only L triggers a split recommendation — because L is the only tier where the estimate indicates the ~200k budget may genuinely be exceeded during implementation.

## For skill/agent authors

This doc is read by both an interactive Claude skill (`skills/refine/SKILL.md`) and a plan-time agent (`agents/planner.md`). Keep references to it as short trigger lines ("size against the budget in `docs/ticket-sizing.md`; split only on real budget risk (tier L)") rather than restating the tiers or the split rule inline — the tiers and rule live here, once, so wording doesn't drift between touchpoints.
