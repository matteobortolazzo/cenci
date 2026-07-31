# Followup triage

How the `Followup` label is meant to behave, and how the `/cenci:maintain backlog` mode keeps its backlog small. Read this before adding, closing, promoting, or grouping `Followup` tickets, or before touching `modes/backlog.md` / `backlog-maintainer`.

## The invariant

`Followup` marks an **untriaged capture queue**, not committed work. An item enters the queue when a pipeline defers it (`implement` Phase 9, or `address-review`'s Acknowledge action). It is a raw capture — unscoped, unestimated, not promised to anyone.

- **Never committed work.** A `Followup` ticket is a candidate for work, not a plan to do it. Nothing downstream treats the queue as a to-do list that must be drained.
- **Never release-blocking.** "Release" in this repo is a per-plugin semver tag auto-applied on push to `main` (paths `flow/**`, `watch/**`, `sandbox/**`). There is **no release gate** anywhere that reads issue labels, so a `Followup` ticket can never hold a release. A shipped release with open `Followup` tickets is the normal, expected state.
- **Removed on triage, not preserved forever.** The label comes off when a human decides the item's fate — promoted to real work, grouped into a polish ticket, or superseded as a duplicate. It is not a permanent mark on the ticket.

## Duplicate-detection scope

When looking for duplicates, compare each open `Followup` against **all open issues**, not just other `Followup` tickets. The same finding is frequently re-captured across several sessions, and an already-refined, non-`Followup` ticket may already own the work. A `Followup` whose root cause is already tracked elsewhere is a duplicate regardless of the other ticket's labels.

## Promote

Promoting a `Followup` to real work means **removing the `Followup` label only**. Do not auto-apply `Refined` (or any other lifecycle label) — refinement is `/cenci:refine`'s job and requires its scoping/acceptance-criteria pass. A promoted ticket re-enters the backlog as a normal, still-unrefined ticket that a human can pick up with `/cenci:refine` when it is worth doing.

## Batch and supersede

Multiple small, independent surviving items that each fit comfortably in one implementation pass should be **grouped into a single polish ticket** rather than left as separate `Followup` tickets. Size the combined ticket against the existing tiers in `docs/ticket-sizing.md:42-47` — multiple small independent concerns fitting one pass should not be split back out.

Grouping mechanics (all approval-gated, run by `/cenci:maintain backlog`):

- The new polish ticket is created **without** the `Followup` label — a human chose to consolidate it, so it enters the backlog as a normal unrefined ticket. Its body cites each source ticket and carries a `Supersedes #a #b #c` line plus the sizing rationale.
- Each source ticket is then closed with a `Superseded by #<new> — consolidated via /cenci:maintain backlog.` comment. This is grouping, not deletion: every source item is preserved verbatim in the consolidated ticket before its source is closed.
- A pure duplicate (same root cause as one existing issue, nothing to combine) is closed with a `Duplicate of #<original>.` comment instead.

## No expiry

There is deliberately **no staleness sweep and no auto-close of old `Followup` tickets**. Age is not a triage signal here; a six-month-old capture can still be the right thing to do. The backlog is kept small by capturing fewer items at the source and by grouping/superseding duplicates during consolidation — never by deleting items for sitting too long.
