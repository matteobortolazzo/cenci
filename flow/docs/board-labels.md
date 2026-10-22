# Board labels

The board lifecycle labels have one home: the `## Labels` table below. It holds each label's
color, description and applier. Skills that create a label read this doc (as "the plugin's
`docs/board-labels.md`") instead of carrying their own table.

## Ensure-then-add procedure

`gh issue edit --add-label` fails when the label does not exist in the repository, so every
path that adds a label follows the same ensure-then-add rule:

1. Ensure the label exists with the color and description from the table, creating it only when
   it is missing: `gh label create "<name>" --repo <owner>/<repo> --color "<color>"
   --description "<description>"`. Never modify or recolor a label that already exists.
2. Add the label with `gh issue edit`.

`/cenci:configure` step 3c (in `skills/configure/phases/guidance-settings.md`) runs step 1 for the whole set up front (`gh label list` once, then one
`gh label create` per missing label). The skills that add a label later repeat step 1 for that one
label just before adding it, so a repository configured before a label was introduced still
works.

The inline `gh label create "<name>" … --color "<color>" --description "<description>"` commands
in the skills stay where they are: they are exact, allow-listed commands that run without an
extra read. `flow/tests/board-labels-drift-contract.test.sh` keeps every such literal identical to
its row here, so a change to a color or description is made in this table first and the test then
points at every literal that must follow.

## Labels

| Label | Color | Description | Applied by |
|---|---|---|---|
| `Working` | `FBCA04` | Actively being refined or implemented | refine (after questions are resolved) / implement (at start) |
| `Refined` | `0E8A16` | Ready for implementation | refine |
| `Planned` | `1D76DB` | Plan on disk, ready to pick up | implement Phase 1 (plan persisted) |
| `In Review` | `A2EEEF` | PR open, under review / CI running | implement Phase 9 (at PR-open) |
| `Implemented` | `6F42C1` | PR merged — done | babysit (on PR merge) |
| `Input Needed` | `D4C5F9` | Planner escalated a question — answer on the ticket to auto-resume | implement's unattended lean-mode escalation path (`cenci pipeline label --transition input-needed`) |
| `Followup` | `C5DEF5` | Deferred/out-of-scope item captured from a session — triage before working | implement Phase 9 / address-review |
| `Browser` | `BFD4F2` | Implementation needs interactive browser access (Playwright CLI) | refine |
| `ui:visual-check` | `FEF2C0` | Visual/layout change — verify in a browser before merge | refine |
| `automerge:ok` | `006B75` | Repository policy granted hands-off merge at refinement — babysit may merge this PR without review | refine (per-ticket grant under `refinement.automerge: "eligible"`) |
| `dispatch-failed` | `b60205` | cenci: dispatched work failed after exhausting its retry budget | cenci dispatch reconciler |
| `plan-invalid` | `d93f0b` | cenci: ticket is Planned but has no parseable plan file | cenci dispatch reconciler |
| `reconcile-stuck` | `5319e7` | cenci: reconciliation itself is stuck (apply-retry budget exhausted) | cenci dispatch reconciler |

`Refined`, `Planned`, `Working`, `In Review` and `Implemented` are the board columns of the
`New → Refined → Planned → Working → In Review → Implemented` lifecycle. A ticket judged
trivial by implement's Trivial-Ticket Triage still transits through `Planned`: same labels, no
new label state, just collapsed into one session instead of a stop-and-relaunch between
`Planned` and `Working`. The other rows are orthogonal markers, never part of that chain:

- `Followup` is a capture-queue marker, never release-blocking; it is removed when the item is
  triaged out of the queue (see `docs/followup-triage.md`).
- `Browser`, `ui:visual-check` and `automerge:ok` are refine-applied markers.
  `automerge:ok` in particular is a per-ticket grant made after eligibility checks and is never
  inherited by split children or followups.
- `Input Needed` marks a ticket waiting for an answer; it is removed (swapping back to `Working`)
  when the ticket resumes.
- `dispatch-failed`, `plan-invalid` and `reconcile-stuck` are applied by the dispatch reconciler,
  not by refine or implement.

## How existence is ensured

The labels reach the repository by one of three routes. Routes 1 and 3 write the color and
description from the table above; route 2 does not.

1. **Skill-level fallback** — `gh label create … || true` in the skill, just before adding the
   label, with the table's color and description. refine ensures `Working`, `Refined`, `Browser`,
   `ui:visual-check` and `automerge:ok`.
2. **CLI self-heal** — `cenci pipeline label` creates the label through the Go `ghLabelCreate`
   when it applies a transition. This covers implement's `Working`, `Planned` and `In Review`,
   plus `Input Needed`. It is not a skill-level `|| true` fallback, and it runs
   `gh label create <name> [--repo <slug>]` with no color or description, so the label gets gh's
   defaults (default color, empty description).
3. **Reconciler self-heal** — the dispatch reconciler's `GHMutator.EnsureLabels` creates
   `dispatch-failed`, `plan-invalid` and `reconcile-stuck` with the table's values
   (`managedLabelSpecs`). Babysit's Go code self-heals `Implemented` with its own spec, and
   `cenci pipeline followup` self-heals `Followup` with the table's color and description before
   it creates a ticket (so implement Phase 9 and address-review carry no `Followup`
   `gh label create` of their own).

`/cenci:configure` step 3c (in `skills/configure/phases/guidance-settings.md`) additionally creates every missing label up front with the table's
color and description, so a board that skips it is not blocked: the routes above heal the label
when it is first needed. The trade-off is route 2: a repository that never ran
`/cenci:configure` can carry gh's default color and an empty description for `Working`,
`Planned`, `In Review` and `Input Needed` until configure runs (configure only creates missing
labels and never recolors an existing one, so it does not repair a label route 2 already created).

The drift test scans markdown skills only. Shell and Go appliers — for example
`flow/skills/close/scripts/close-parent.sh` and `watch/internal/babysit/parent.go` — are outside
it, so a color or description there is not checked against the table.
