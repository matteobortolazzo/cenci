# Codex refine procedure

Read `project-core` and `codex-runtime`. Require `/plan`. Gather ticket context, classify
frontend/design scope, and produce the refined ticket proposal. Ask ONLY about product
decisions, architecture decisions with a real trade-off, or contradictions/unknowns the
codebase cannot resolve — everything else with an obvious recommended answer must be
auto-adopted, never asked, into the proposal's `### Assumptions (auto-adopted)` section
(plain `-` bullets, never task-list checkboxes: `- <assumption> — <adopted answer and why
it is obvious>`).

every question with options marks one recommended option first with a one-line rationale, and every open-ended question leads with the refiner's proposed answer.

entailed questions — those already fixed by a recorded answer — are forbidden; auto-adopt them into `### Decisions` with a `follows from Q<n> (round <m>)` citation, and when the entailed decision fixes a security posture or is otherwise irreversible, ask via the client's available user-input mechanism a confirm/overrule question that states the decision and its derivation without re-opening the full option space. This confirm/overrule question is exempt from any per-round question cap and must be asked before a round can conclude with no remaining questions — never deferred, never silently dropped.

Also carry a `### Decisions` section (integration points, error-handling
convention, backward-compatibility decision, plus any other settled decision) — both
sections persist into the ticket body alongside `### Acceptance Criteria`, and the planner
inherits them verbatim and must not re-open them. Carry a per-ticket `### Automation` verdict
registry — one line for the parent (`automerge (parent): grant|withhold — <rationale>`) plus
one line per proposed split child (`automerge (K/N) <child title>: grant|withhold —
<rationale>`) — withhold by default for security-sensitive paths, release/CI workflow files,
visually verifiable UI work, or irreversible migration/data changes, and whenever uncertain,
applied independently per ticket; this section is not written into the ticket body. A split's
`### Suggested Split` carries each child as a decision-complete block (`### Goal`,
`### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance Criteria`,
`### Dependencies`) so it is plannable without undocumented parent context. Do not edit
GitHub in Plan mode. Hand off `$cenci:refine apply <ticket> <approved-plan>` to normal mode.

The pre-confirmation phase performs only read-only GitHub calls (`gh issue view`, `gh api
user --jq .login`) and local temp-file writes — no ownership claim, no `Working` label, and no
ticket/label/sub-issue mutation of any kind runs before the gate below confirms.

**Split-depth guard**: before analyzing, determine split-child provenance — primary source is
the native sub-issue link, `gh issue view <number> --repo <owner>/<repo> --json parent --jq '.parent.number // empty'`
(a returned number means this ticket is a split child of that parent); fallback for older
convention-linked tickets, or a non-zero primary command, is a `Related to #<number>` first
non-empty body line. A split child is presumed sized by its parent's refinement — split depth
is one, and grandchild tickets are never created (`docs/ticket-sizing.md`) — so
never emit `### Suggested Split` for it, regardless of the size estimate. If analysis still concludes L,
keep the honest L verdict in `### Size Estimate` with an explicit recommendation to
re-partition the parent instead of splitting further, and the Confirmation Gate below must
then ask, via the client's available user-input mechanism, whether to
proceed with the oversize child as-is or decline so the parent's partition can be redone —
a decline performs zero GitHub writes, and re-running refine against the parent is how to
redo its partition.

**Confirmation Gate (apply mode, before any GitHub write)**: no ticket, label, or sub-issue mutation of any kind — including the ownership claim and the `Working` label — happens until
this gate confirms. For
each proposed split child, apply the `frontend-classification` reference skill to that child's
own block text to determine whether it needs a scoped browser question (skipped entirely for a
design-only child) — the parent's own browser question is independent and is never propagated
to any child. Compute each ticket's effective `automerge:ok` grant (`### Automation` verdict is
exactly `grant` AND NOT `isDesignTicket` AND NOT `browserRequired` AND NOT the `ui:visual-check`
signal match, evaluated independently per ticket; fail-closed to `withhold` on an absent/other
value) and each ticket's final label set (parent per the label edit below; each child = inherited
non-excluded parent labels + `Refined` [+ `Design`] [+ `Browser`] [+ `ui:visual-check`] [+
`automerge:ok` when granted]). Render the complete proposal plus a per-ticket manifest (title,
label set, milestone, intended hierarchy/dependencies, grant/withhold + rationale, plus the
parent's own pending ownership-claim and `Working` transition), then ask, via the client's
available user-input mechanism, "Apply this refinement as shown?" with Confirm/Decline options —
no adjust loop. A **Decline** performs zero GitHub writes and requires no cleanup mutation: title,
body, labels, assignees, milestone, and native sub-issues are state-for-state unchanged, and
re-running refine is how to adjust. Only a Confirm proceeds to the write phase below.

Once confirmed, every write proceeds in this order: claim → Working → parent body →
children+links → Pass 2/design → Refined/-Working → ui:visual-check (see `### Write order`
at the end of this file). Before any of those writes, re-fetch the parent's milestone/labels
(unconditionally, even on a parent-only run) and re-verify exclusive ownership; a conflict on
the re-verify stops with zero writes, same as the pre-confirm check. Diff the re-fetched labels
against the gate-time snapshot: **authorization-sensitive drift** (`automerge:ok`, `Browser`, or
`ui:visual-check` changed on the parent) stops the run with zero writes and asks for a fresh
`$cenci:refine apply` from scratch — no in-session re-gate; **cosmetic drift** (milestone,
`area:*`, priority, team, `Design`, or any other label) proceeds using the freshly fetched
snapshot and discloses the drift in the final message.

**`automerge:ok` grant (apply mode, parent ticket)**: as part of the same label edit
that applies `Refined`/`Design`/`Browser`, use the effective grant computed at the
Confirmation Gate above (do not recompute) — ensure the label exists (`gh label create
"automerge:ok" --repo <owner>/<repo> --color "006B75" --description "Human granted
hands-off merge at refinement — babysit may merge this PR without review" 2>/dev/null ||
true`), then append `--add-label "automerge:ok"` when the effective grant holds, or
`--remove-label "automerge:ok"` when it does not and the issue currently carries the label
(re-refine), or nothing otherwise. Every proposed split child gets its own independently
computed grant/withhold from the same gate, applied when that child is created (see below) —
never inherited from the parent.
Before the Confirmation Gate renders its manifest, when a split is proposed, first verify each child block is structurally complete — every child in the
adopted `### Suggested Split` has all five subsections present (`### Goal`, `### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance criteria`, `### Dependencies`), each satisfying its emptiness
rule: `### Goal` non-empty prose; `### Dependencies` non-empty ("None." valid); `### Decisions` and
`### Assumptions (auto-adopted)` each non-empty or exactly "None."; `### Acceptance criteria` empty only
for a child the partition assigned zero criteria; a missing or empty-violating child aborts the split
before any GitHub write, before the gate renders a manifest, and before the acceptance-criteria partition check runs. This structural check only
confirms presence/absence and does not itself judge whether an empty `### Acceptance criteria` section is
legitimate — the partition check below is the sole verifier of correct assignment, and a child wrongly left
empty will surface there as an unassigned criterion. Only then verify the
proposal partitions the parent's acceptance criteria:
every parent criterion assigned to exactly one child (integration-scoped criteria on a child that
depends on all others); an unassigned or duplicated criterion aborts the split before any GitHub
write. Each child body then carries its own `### Acceptance Criteria` section — its slice of the parent's partition —
after the dependency lines and description, plus that child's own `### Decisions` and
`### Assumptions (auto-adopted)` persisted from its `### Suggested Split` block.

**Creation checkpoint (idempotent create/recover/repair/link, #876)**: every split child and the
companion design ticket are created through `"${PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh"`
— invoked exactly as this same script is invoked from Claude's SKILL.md, and exactly as
`configure/codex.md:12` invokes `detect-project.sh` — via its `ensure-issue.sh init`,
`ensure-issue.sh ensure`, `ensure-issue.sh link`, and `ensure-issue.sh clear` subcommands. This
makes creation recoverably idempotent across timeouts, retries, crashes, and a resumed apply-mode
run: each manifest entry mints a nonce at `init` time and embeds a hidden
`<!-- cenci-refine-create:<nonce> -->` marker in the created issue's body, so a resumed run
recovers the same issue by re-scanning for that exact marker instead of re-creating blind.

Before the first create, run `"${PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh" init --repo <owner>/<repo> --parent <parent> --checkpoint .plans/.refine-<parent>.checkpoint.json --manifest <manifest-file>`
(add `--parent-meta <parent-meta-file>` — the parent-metadata fetch below is unconditional and fail-closed before any write, so it always succeeds by the time this runs; the flag is always passed, never omitted). The
checkpoint lives at `.plans/.refine-<parent>.checkpoint.json` — keyed by the repo and parent issue
it recovers, not this run, so it survives a crash across separate invocations. For each split
child, run `ensure-issue.sh ensure --checkpoint <path> --repo <owner>/<repo> --slot child-K-of-N
--title <title-file> --body <body-file>` to resolve it to exactly one issue, then
`ensure-issue.sh link --checkpoint <path> --repo <owner>/<repo> --slot child-K-of-N --parent
<parent>` to link it as a native GitHub sub-issue — `link` checks the parent's existing sub-issue
list first (already-linked is a no-op success, never a duplicate `--parent` edit) and verifies
from the parent side before returning success; do not append a child-ticket markdown checklist —
the native sub-issue list carries the enumeration. The companion design ticket uses the same
`init`/`ensure` pair with a single `"design"` slot and no `link` call (it is related via a body
dependency line, not native sub-issue hierarchy).

If the checkpoint is missing or corrupt (bad JSON, wrong schema version) on any call other than
`init`, `ensure-issue.sh` itself exits non-zero and this is by design — it must **fail closed** and
never silently re-create. Treat that, and any other non-zero `ensure-issue.sh` exit, as a hard
stop: report the error and do not create any further children or the design ticket. Once the run
completes successfully, run `ensure-issue.sh clear --checkpoint <path>` (idempotent — a second
`clear` is not an error); an aborted run instead retains the checkpoint so the next attempt
resumes from it rather than re-creating already-created issues.

Every ticket this workflow creates — each split child
and the companion design ticket — inherits the parent's milestone (as the numeric `.milestone.number`,
omitted entirely when the parent has none) and every parent label except the 10 lifecycle/transient
and refinement-granted markers (`Refined`, `Working`, `Planned`, `In Review`, `Implemented`,
`Design`, `Designed`, `automerge:ok`, `Browser`, `ui:visual-check`), on top of its own seed
labels — `automerge:ok`, `Browser`, `ui:visual-check` are never inherited from the parent's
current labels; each child's own copy of those three, if any, comes only from the Confirmation
Gate above; the parent-metadata fetch is unconditional and runs before any write — if it fails
after one retry, the parent cannot be read after one retry, so **stop with zero writes** (D1):
create no tickets, update no ticket body, claim no ownership, add no `Working` label, and report
that re-running `$cenci:refine apply <ticket> <approved-plan>` is how to retry. This inheritance
merge (the `--slurpfile`-based label exclusion and the numeric-milestone-only rule) now runs
inside `ensure-issue.sh init`'s own `--parent-meta` handling rather than being computed inline
here (#876).

Divergence: the refiner agent split is Claude-only — Codex has no subagent model tiering, so
this native procedure performs the refinement analysis inline as described above.

**Command surface (least privilege)**: this workflow's own procedure performs no remote
fetches of its own — neither a `curl` grant nor a web-fetch capability, and the procedure
invokes neither. (Attachment downloads via the `attachments` reference skill may still use
`curl` when the user selects an attachment; that call falls outside this narrowed grant and
will prompt for approval — an accepted tradeoff, not a regression.) Its `gh` surface is
limited to exactly two `gh issue` verbs — `view` and `edit` — and no other verb (refine posts
no comments and never lists/closes an issue), plus `gh label create …`, `gh api user --jq …`
(via `ticket-ownership`), `gh api repos/…`, and `"${PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh"`
itself; its `git` surface is limited to `git remote
get-url` (the script derives nothing from `git` itself — it receives `--repo <owner>/<repo>` as an
argument); and its own payload-composition surface is a standalone `jq -n --rawfile …` call at the
retitle site. Every child-ticket create and the companion design-ticket create now go through
`ensure-issue.sh` rather than this procedure's own inline `gh api` calls (#876): internally the
script composes its create/repair payloads via `jq -n --rawfile …` plus `--slurpfile` for the
parent-metadata label/milestone merge — the same mechanism that lets externally-sourced label
names reach the payload without ever touching a command line, per the `shell-rules` skill's
canonical snippet — and its own `gh` surface (candidate listing via `gh api repos/…/issues?…
--paginate`, create via `gh api repos/…/issues -X POST --input … --jq .number`, repair via
`gh api repos/…/issues/<n> -X PATCH --input …`, and linking via `gh issue edit <child> --parent
<parent>` / `gh issue view <parent> --json subIssues`) stays inside this same documented
least-privilege set — no new verb or prefix. The only temp-name primitive is a standalone
`mktemp -u ${TMPDIR:-/tmp}/cenci/…` call — a dry-run name generator, never `mktemp -d`; the file tool
creates the actual file, and the printed token is carried forward as literal text, never
shell state. Every title-carrying issue write (the retitle edit here, and — inside
`ensure-issue.sh` — each child-ticket create/repair and the companion design-ticket
create/repair) goes through `gh api repos/<owner>/<repo>/… -X
PATCH|POST --input <json-file>` with a payload `jq`-composed from file-tool-authored raw
title/body inputs — never an inline `--title` and never a hand-escaped JSON literal.

### Write order

claim (the ownership claim, first write) → working (the Working label ensure + add) →
parent body (the parent ticket edit) → for each split child in dependency order, child-create
immediately followed by its own child-link, and so on, all before parent-exec-order →
parent-exec-order (the Execution Order note, or the companion design ticket's create when
there is no split) → refined (the Refined label add / Working label removal) → visual-check
(the ui:visual-check label add, skipped when isDesignTicket).

Op tokens, in canonical order for a 2-child split: `claim` `working` `parent-body`
`child-create:1` `child-link:1` `child-create:2` `child-link:2` `parent-exec-order` `refined`
`visual-check`.
