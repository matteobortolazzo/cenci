# Ticket Sizing

Every implementation ticket carries a size of **S**, **M** or **L** in its body — the
`### Size Estimate: <letter>` section written at refinement. The size bounds the single PR
the ticket maps to (1 ticket = 1 PR). It protects three things, in this order: how much one
reviewer pass (bot or human) can read with full attention, how much one implementation lane
must hold in its working set, and how much a rollback undoes. It does not estimate effort or
wall-clock time, and **model capability is never a reason to size up**: a more capable model
earns fewer review rounds per PR, not a larger PR.

This file is cenci's copy of the contract defined in the Velka repository's
`docs/dev/workflow/branches-and-tickets.md` ("Ticket sizes", "Splitting a ticket") and
`docs/agent-workflow.md` ("Lane size and context budget"). Those sections are authoritative;
the numbers below are theirs and must never be restated with different values.

## The size scale

| Size | Shape | Hard proxies for the PR | Review and merge |
|---|---|---|---|
| **S** | One concern in one project; no schema change; no path in the automerge protected list | `<= 10 changed files, <= 400 changed lines` | Bot review; automergeable |
| **M** | One feature slice; API plus UI allowed; at most one migration; one or two delegation lanes | `<= 30 changed files, <= 2,000 changed lines` | Bot review plus founder skim — **the default** |
| **L** | Several coupled slices that cannot ship separately; allowed only with a recorded reason why they cannot ship as a stacked series of M PRs; always several delegation lanes with exclusive file ownership | `<= 60 changed files, <= 5,000 changed lines` | Bot review plus a full founder read |

Changed lines are **insertions plus deletions over the whole PR, generated files and lockfiles
included**. Anything beyond L is **split, never written as one ticket**. The S proxies are the
automerge thresholds in `.cenci/config.json` (`maxChangedFiles` 10 / `maxDiffLines` 400): the
automerge gate stays at the S bound, and the two must change together.

### The bound lines, verbatim

Every `### Size Estimate: <letter>` section — on a refined ticket, in a planner's confirmed
size, and on every split child — writes the size's bound on the line right after the heading,
exactly as below, followed by a `Rationale:` line of one or two sentences:

```
### Size Estimate: S
Bound: <= 10 changed files, <= 400 changed lines, one concern in one project, no schema change, no automerge protected path.
Rationale: <one or two sentences>

### Size Estimate: M
Bound: <= 30 changed files, <= 2,000 changed lines, one feature slice, at most one migration.
Rationale: <one or two sentences>

### Size Estimate: L
Bound: <= 60 changed files, <= 5,000 changed lines, several coupled slices; reason they cannot ship as a stacked series of M PRs: <reason>.
Rationale: <one or two sentences>
```

An L estimate without a filled-in reason is not an L estimate: the refine skill rejects it, and
the planner's Split Gate treats it as a split trigger.

## Where the size is enforced

The estimate is checked at four points, each against the same table:

1. **Refinement** (`skills/refine/SKILL.md`, `agents/refiner.md`) writes the letter with its
   bound and rationale, and proposes a split when the scope exceeds L.
2. **Planning** (`agents/planner.md`, `skills/implement/phases/phase-1-plan.md`): after
   `### Files to Modify`, the planner compares the plan against the ticket's bound in a
   `### Size Check`. A plan that exceeds the bound stops before any implementation mutation
   and returns a split proposal — **growth during planning is a split trigger**, not a reason
   to continue. The confirmed size is recorded in the plan.
3. **Implementation lanes** (`skills/implement/phases/phase-3-test-red.md`,
   `phase-4-implement-green.md`, `agents/implementer.md`): each lane in the plan's
   `### Delegation Preflight` is at most one M; an L ticket is always several lanes with
   exclusive file ownership, never one lane.
4. **PR creation** (`skills/implement/phases/phase-9-pr.md`, `scripts/pr-size.sh`): the final
   diff's file and line counts are classified and written as
   `Size: <letter> (<files> files, <lines> lines) vs ticket <letter>` in the PR body. A PR one
   size above its ticket is flagged in the body and to the founder, and must not be merged on
   green checks alone.

## Lane size and context budget

An implementation lane is at most one M ticket's worth of change; an L ticket is delivered as
several lanes with exclusive file ownership. Size a lane so the worker finishes using **about
half of the model's advertised context window**; the ceiling itself is not a sizing unit.
Quality degrades well before a window fills (skipped enumerated items, forgotten guide rules),
and automatic compaction is disabled (`autoCompactDisabled` in `.cenci/config.json`), so a lane
that nears its ceiling stops rather than degrading gracefully. Every implementer is briefed
with exactly this budget. A worker that reports running low on context, or a lane that needs a
second resume, is evidence the ticket is one size larger than written: the orchestrator reports
it to the root agent as a **split signal** and stops — it never retries with a longer lane.

The `haiku` agent tier is hard-capped at 200k tokens regardless of the session model, and
`context-gatherer` runs first in the implement pipeline, so a ticket whose bundle overflows that
tier fails at the first step. This is a second, independent reason the S/M/L proxies above are
not a line-count-only concern.

## Why this is estimated, not measured

`/cenci:refine`'s refiner agent explores the codebase (Glob/Grep) before producing a size
estimate, and grounds a split's per-child sizing in a bounded enumeration of that child's
affected files/components — cited by name in the child's size reasoning. That exploration is
still not an implementation pass: neither refine nor the `planner` agent can count a diff that
does not exist yet. The planner treats a refine-time `### Size Estimate` already persisted in
the ticket body as a **prior** — it inherits that estimate rather than re-deriving from scratch,
and overrides it only with genuinely new evidence beyond what refine enumerated, naming
specifically what changed. The PR step is the only point where the size is measured rather than
estimated, which is why a mismatch there is a reviewable flag rather than a silent correction.

## When to split

Split a ticket when its scope exceeds the L bound, when the planner's `### Size Check` finds the
plan exceeds the ticket's bound, or when an implementation lane reports the context-budget
signal above. Do **not** split on any of these grounds alone:

- The ticket touches multiple independent concerns, but each is small and the whole fits M —
  M is the default and allows API plus UI in one slice.
- Splitting would be "cleaner" or more theoretically parallelizable — parallelism is a
  Delegation Preflight concern (lanes inside one ticket), not a reason for more tickets.
- The ticket could be organized into logical commits — that's what multi-commit PRs are for.

Splitting is a **coverage exercise**, not a work-division exercise: every acceptance criterion,
edge case, verification step, documentation item and boundary of the parent gets exactly one
child owner, the parent keeps its original items verbatim and gains a `### Coverage map`, and
a split parent closes only after a coverage review against the merged child PRs (see the
`refine` and `close` skills).

## Split children are presumed sized

Split depth is one. A ticket created as a child of a split — a native GitHub sub-issue, or an
older ticket carrying the `Related to #<parent>` first-body-line convention — was already sized
when its parent's partition was designed, and refinement must never split it again, regardless
of its own size estimate. Re-splitting diverges instead of converging: each refinement round adds
structure (decision-complete child blocks, per-child criteria, dependency lines), which inflates
the estimate, and a guardless pipeline splits the same work 3-4 levels deep without any real
signal changing.

When a split child still sizes L, that is a signal the **parent's partition was wrong**, not a trigger for another split.
The refiner keeps the honest L verdict and recommends re-partitioning
the parent in its `### Size Estimate` rationale; the refine skill then surfaces the choice to a
human — proceed with the oversize child as-is, decline so the parent's partition can be redone,
or explicitly authorize splitting the child anyway. Grandchild tickets are never created
automatically.

### Human-authorized exception

Split depth is hard-capped at one by default because the #929 incident was about automatic recursive splitting with no human in the loop —
the guardless pipeline reached its own conclusion
and acted on it, repeatedly, 3-4 levels deep, without a person deciding anything. A human
explicitly choosing to split an oversize child anyway does not reintroduce that failure mode: the
refine skill's Oversize split child escalation offers a third option, alongside Proceed and
Decline, that a human must actively select in that run, for that specific child, before a
resulting grandchild ticket is ever created. The refiner never proposes this on its own — it only
evaluates a split for a split child when the invocation explicitly carries
`resplitAuthorized: true`, which is set solely by that escalation's third option, never inferred
and never carried over from a prior run.

This does not relax the "split depth is one" default rule — the default is unchanged, and every
split child is still presumed sized and never automatically re-split. This is a narrow,
explicit, human-triggered exception to that default, not a new default: a grandchild created
this way is still just a split child one level down from an already-split child, so split depth
stays bounded at two, and its existence is traceable to one specific human decision rather than
to an unattended pipeline run. The refiner treats an authorized resplit exactly like a normal
split once authorized — same Coverage gate, same Confirmation Gate manifest and single
confirmation, same Pass 1/Pass 2 child creation — no special-cased bypass of any of those checks.

## Mapping to S/M/L

- **S** — one concern in one project. One ticket, one PR, no split; eligible for automerge.
- **M** — one feature slice. One ticket, one PR, no split. The default.
- **L** — several coupled slices. One ticket, one PR, several lanes; only with a recorded reason
  why the slices cannot ship as a stacked series of M PRs. An L estimate on a split child never
  triggers a split — it triggers the parent re-partition escalation above.
- **Beyond L** — split, never written as one ticket.

## For skill/agent authors

This doc is read by an interactive Claude skill (`skills/refine/SKILL.md`), a plan-time agent
(`agents/planner.md`), the implementation phases, the acceptance reviewer, and the `close`
skill. Keep references to it as short trigger lines ("size against the table in
`docs/ticket-sizing.md`") and quote the bound lines verbatim from the block above rather than
restating the numbers in prose — the table lives here, once, so wording cannot drift between
touchpoints. `scripts/pr-size.sh` under `skills/implement/` is the executable form of the same
table; a change to the bounds updates the Velka source, this file, that script, and
`.cenci/config.json`'s S-bound automerge thresholds together.
