# Codex refine procedure

Read `project-core` and `codex-runtime`. Require `/plan`. Gather ticket context, classify
frontend/design scope, and produce the refined ticket proposal. Ask ONLY about product
decisions, architecture decisions with a real trade-off, or contradictions/unknowns the
codebase cannot resolve — everything else with an obvious recommended answer must be
auto-adopted, never asked, into the proposal's `### Assumptions (auto-adopted)` section
(plain `-` bullets, never task-list checkboxes: `- <assumption> — <adopted answer and why
it is obvious>`). Also carry a `### Decisions` section (integration points, error-handling
convention, backward-compatibility decision, plus any other settled decision) — both
sections persist into the ticket body alongside `### Acceptance Criteria`, and the planner
inherits them verbatim and must not re-open them. Carry an `### Automation` verdict
(`grant`/`withhold` plus a one-line rationale) — withhold by default for security-sensitive
paths, release/CI workflow files, visually verifiable UI work, or irreversible
migration/data changes, and whenever uncertain; this section is not written into the ticket
body. Do not edit GitHub in Plan mode. Hand off `$cenci:refine apply <ticket>
<approved-plan>` to normal mode, then update the ticket and labels and clear the checkpoint.

**`automerge:ok` grant (apply mode, parent ticket only)**: as part of the same label edit
that applies `Refined`/`Design`/`Browser`, compute the effective grant — refiner verdict is
grant AND NOT isDesignTicket AND NOT browserRequired AND NOT the `ui:visual-check` signal
match — ensure the label exists (`gh label create "automerge:ok" --repo <owner>/<repo>
--color "006B75" --description "Human granted hands-off merge at refinement — babysit may
merge this PR without review" 2>/dev/null || true`), then append `--add-label
"automerge:ok"` when the effective grant holds, or `--remove-label "automerge:ok"` when it
does not and the issue currently carries the label (re-refine), or nothing otherwise.
When a split is applied, first verify the proposal partitions the parent's acceptance criteria:
every parent criterion assigned to exactly one child (integration-scoped criteria on a child that
depends on all others); an unassigned or duplicated criterion aborts the split before any GitHub
write. Each child body then carries its own `### Acceptance Criteria` section — its slice of the parent's partition —
after the dependency lines and description. Link each child of the split to the parent as a native GitHub sub-issue
(`gh issue edit <child> --parent <parent>`) — do not append a child-ticket markdown checklist; the
native sub-issue list carries the enumeration. Every ticket this workflow creates — each split child
and the companion design ticket — inherits the parent's milestone (as the numeric `.milestone.number`,
omitted entirely when the parent has none) and every parent label except the 7 lifecycle markers
(`Refined`, `Working`, `Planned`, `In Review`, `Implemented`, `Design`, `Designed`), on top of its own
seed labels; if the parent-metadata fetch fails after one retry, create the tickets without inheritance
and say so in the final message rather than aborting the split.

Divergence: the refiner agent split is Claude-only — Codex has no subagent model tiering, so
this native procedure performs the refinement analysis inline as described above.

**Command surface (least privilege)**: this workflow's own procedure performs no remote
fetches of its own — neither a `curl` grant nor a web-fetch capability, and the procedure
invokes neither. (Attachment downloads via the `attachments` reference skill may still use
`curl` when the user selects an attachment; that call falls outside this narrowed grant and
will prompt for approval — an accepted tradeoff, not a regression.) Its `gh` surface is
limited to exactly two `gh issue` verbs — `view` and `edit` — and no other verb (refine posts
no comments and never lists/closes an issue), plus `gh label create …`, `gh api user --jq …`
(via `ticket-ownership`), and `gh api repos/…`; its `git` surface is limited to `git remote
get-url`; and its payload-composition surface is a standalone `jq -n --rawfile …` call — plus
`--slurpfile` on the two creating sites, which is how the parent's externally-sourced
label names reach the payload without ever touching a command line — per
the `shell-rules` skill's canonical snippet. The only temp-name primitive is a standalone
`mktemp -u ${TMPDIR:-/tmp}/cenci/…` call — a dry-run name generator, never `mktemp -d`; the file tool
creates the actual file, and the printed token is carried forward as literal text, never
shell state. Every title-carrying issue write (the retitle edit, each child-ticket create,
and the companion design-ticket create) goes through `gh api repos/<owner>/<repo>/… -X
PATCH|POST --input <json-file>` with a payload `jq`-composed from file-tool-authored raw
title/body inputs — never an inline `--title` and never a hand-escaped JSON literal.
