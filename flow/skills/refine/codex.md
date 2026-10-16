# Codex refine procedure

Read `project-core` and `codex-runtime`. Require `/plan`. Gather ticket context, classify
frontend scope, and produce the refined ticket proposal. Ask ONLY about product
decisions, architecture decisions with a real trade-off, or contradictions/unknowns the
codebase cannot resolve — everything else with an obvious recommended answer must be
auto-adopted, never asked, into the proposal's `### Assumptions (auto-adopted)` section
(plain `-` bullets, never task-list checkboxes: `- <assumption> — <adopted answer and why
it is obvious>`).

Resolve `refinementAutonomy` on every invocation, both `/plan` and `apply`, from the config `project-core` returns: it reads `refinement.autonomy`. Only the exact string `"lean"` means lean. An absent key, a missing or non-object `refinement` block, or any other value means `interactive`, and refine then behaves exactly as it did before lean mode existed. `planning.autonomy` and `planning.attended` are never consulted. When `refinement.autonomy` is present but is not exactly `"lean"` or `"interactive"`, print one notice line, `refinement.autonomy "<value>" unrecognized; running interactive`, and continue in interactive mode — the mode is still `interactive`.

every question with options marks one recommended option first with a one-line rationale, and every open-ended question leads with the refiner's proposed answer.

entailed questions — those already fixed by a recorded answer — are forbidden; auto-adopt them into `### Decisions` with a `follows from Q<n> (round <m>)` citation, and when the entailed decision fixes a security posture or is otherwise irreversible, ask via the client's available user-input mechanism a confirm/overrule question that states the decision and its derivation without re-opening the full option space. This confirm/overrule question is exempt from any per-round question cap and must be asked before a round can conclude with no remaining questions — never deferred, never silently dropped.

**Lean refinement** (only when `refinementAutonomy` is `lean`; interactive refine follows every rule above unchanged, never emits a `decided:` entry, and keeps its own per-round cap of at most 4 questions). In lean mode ask only in four classes: security-sensitive, destructive or irreversible, scope blowup (split), and product ambiguity with no recommendable option. Recommendable means exactly one option is justifiable from the repo, with a rationale citing the ticket, the codebase, or project docs by repo-relative path or identifier. Architecture trade-offs, resolvable contradictions, and recommendable product choices are recorded in `### Decisions` as `decided:` entries instead of being asked: `decided: <answer> — <rationale> (alternative: <option label>)`, naming the main alternative not taken. In lean mode a ticket with only conventional decisions reaches the Confirmation Gate with zero questions asked — every such decision is recorded in `### Assumptions (auto-adopted)` or `### Decisions`, never silently dropped. Lean mode allows one question round and at most 3 questions, asked via the client's available user-input mechanism, and after round 1 only cap-exempt questions are asked. Tag every non-exempt question with exactly one class tag: `(security)`, `(destructive)`, `(split)`, or `(product)`. An untagged question ranks last and counts against the cap, and its class is `product` for the over-cap suffix and ordering. The one exception to the cap is the confirm/overrule question tagged `(confirm/overrule — cap-exempt)`: it is always asked and never dropped, even when the 3-question cap is already full, and it does not count toward the 3. A security-sensitive or irreversible entailed decision still gets its cap-exempt confirm/overrule question, even when the cap is already full. When more than 3 non-exempt questions are warranted, sort them by class tag and rank them confirm/overrule, security, destructive, split, product ambiguity; the top 3 are asked. A question over the cap is never asked: record it as a `Q:` / `A:` pair whose answer is the overflow sentinel and decide it under the overflow rule — the sentinel is `Not asked — over the lean question cap; decide it under the overflow rule.` — recording the decision as `decided: <answer> — <rationale> (alternative: <option label>) (over the cap: <class>)`. Non-exempt questions are decided this way at most once: if the analysis after round 1, after the Confirmation Gate's overrule, or after the `resplitAuthorized` re-run still produces a non-exempt question, STOP with zero GitHub writes and report a lean-contract violation. A cap-exempt question whose wording is identical to one already answered is not asked again: the recorded answer is re-fed once, and if the analysis repeats the question after that, STOP with zero GitHub writes and report a lean-contract violation. At most 3 cap-exempt-only rounds follow round 1 in each pass (the question loop is one pass, the overrule pass another); a further round is a lean-contract violation, so STOP with zero GitHub writes and report it. Lean mode only reduces or reorders questions; it never removes a confirmation, a stop, or a fail-closed default. Lean mode does not change the `Browser` derivation: it is still derived from classification, never asked, and the `Browser` label is still applied from it. Lean mode never grants automerge by itself — the `### Automation` verdicts and the Confirmation Gate's fail-closed grant computation are identical in both modes.

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
`### Size`, `### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance Criteria`,
`### Dependencies`) so it is plannable without undocumented parent context — each child's
`### Size` is grounded in a bounded enumeration of that child's affected files/components
found during exploration, and is always S or M, never L (an L split child means the parent's
partition was wrong, not that the child needs further splitting). Do not edit
GitHub in Plan mode. Hand off `$cenci:refine apply <ticket> <approved-plan>` to normal mode.

The pre-confirmation phase performs only read-only GitHub calls (`gh issue view`, `gh api
user --jq .login`) and local temp-file writes — no ownership claim, no `Working` label, and no
ticket/label/sub-issue mutation of any kind runs before the gate below confirms.

**Split-depth guard**: before analyzing, determine split-child provenance — primary source is
the native sub-issue link, `gh issue view <number> --repo <owner>/<repo> --json parent --jq '.parent.number // empty'`
(a returned number means this ticket is a split child of that parent); fallback for older
convention-linked tickets, or a non-zero primary command, is a `Related to #<number>` first
non-empty body line. A split child is presumed sized by its parent's refinement — split depth
is one, and grandchild tickets are never created automatically (`docs/ticket-sizing.md`) — so
never emit `### Suggested Split` for it, regardless of the size estimate, unless `resplitAuthorized` was set earlier in this run.
That exception is granted only by the human explicitly choosing the Confirmation Gate's third
oversize-escalation option below, and by no other means; nothing else ever sets it, and the
analysis itself never sets it. If analysis still concludes L and `resplitAuthorized` is not set,
keep the honest L verdict in `### Size Estimate` with an explicit recommendation to
re-partition the parent instead of splitting further, and the Confirmation Gate below must
then ask, via the client's available user-input mechanism, whether to
proceed with the oversize child as-is or decline so the parent's partition can be redone —
or explicitly authorize splitting this child anyway (human-authorized; creates grandchildren).
A decline performs zero GitHub writes, and re-running refine against the parent is how to
redo its partition; authorizing the third option is the only way `resplitAuthorized` is ever
set — it sets it for the remainder of this run and re-runs the analysis (in lean mode that re-run is covered by the lean loop guard above: any non-exempt question it produces is a lean-contract violation), routing any resulting
`### Suggested Split` through the same Confirmation Gate and write phase as any other split
proposal, with no special-cased bypass. Because the ask above is scoped to `resplitAuthorized`
not already being set, a re-derived proposal that still concludes L on the same split child
does not re-trigger the ask on this second pass — it flows straight to the Confirmation
Gate's normal manifest/confirm step instead, which is what prevents an infinite re-ask loop.

**Confirmation Gate (apply mode, before any GitHub write)**: For a split, follow `parent-blockers.md` before rendering the manifest. Fetch and validate the parent's native blockers and select only OPEN issues. Each child's effective blockers are the deduplicated union: **inherited open parent blockers + blocking siblings**. Include that set in every child's manifest, including the first child. Repeat the shared revalidation before the first write; a changed open set requires fresh confirmation.

**Mutation boundary:** no ticket, label, or sub-issue mutation of any kind — including the ownership claim and the `Working` label — happens until
this gate confirms. For
each proposed split child, apply the `frontend-classification` reference skill to that child's
own block text and derive its `Browser` requirement from that classification (frontend/UI, or a
mention of web scraping, browser automation, or manual browser testing) — never ask the user;
the parent's own `browserRequired` is derived the same way from the parent ticket, is
independent, and is never propagated
to any child. Compute each ticket's effective `automerge:ok` grant (`### Automation` verdict is
exactly `grant` AND NOT `browserRequired` AND NOT the `ui:visual-check`
signal match, evaluated independently per ticket; fail-closed to `withhold` on an absent/other
value) and each ticket's final label set (parent per the label edit below; each child = inherited
non-excluded parent labels + `Refined` [+ `Browser`] [+ `ui:visual-check`] [+
`automerge:ok` when granted]). Render the complete proposal plus a per-ticket manifest (title,
label set, milestone, intended hierarchy/dependencies, grant/withhold + rationale, plus the
parent's own pending ownership-claim and `Working` transition), then ask, via the client's
available user-input mechanism, "Apply this refinement as shown?" with Confirm/Decline options. In interactive mode the gate is unchanged — no adjust loop. In lean mode the render also numbers every `### Assumptions (auto-adopted)` item and every `decided:` entry continuously as `A1…An`. The numbering is display-only; the persisted `### Assumptions (auto-adopted)` keeps plain `-` bullets. The list opens with a group headed `Decided over the cap — review before confirming`, which lists the entries decided over the cap first, under that heading, with security and destructive entries first; an entry is over-cap when it ends in the `(over the cap: <class>)` suffix, and its `<class>` orders it. This procedure orders and reconciles that group from its own record of the questions it withheld and the class tags it saw (an untagged question's class is `product`), not only from the suffix on the re-emitted entries: it matches each withheld question to its over-cap entry, takes the class from its own record when the suffix is missing or disagrees, and reconciles after every analysis pass, including the overrule re-analysis. Whenever a withheld question has no matching over-cap entry, list it under the same heading, marked "no recorded decision", so nothing withheld is invisible at Confirm. In lean mode a freeform "Other" answer to the confirmation question is an overrule, not a decline. A freeform answer is always an overrule, even when its text reads like "no" or "decline". Empty or whitespace-only "Other" text is treated as Decline. The overrule is available once per run: it is recorded as `Q: Confirmation Gate overrule` / `A: <text>`, and the procedure re-runs the analysis inline exactly once, read-only, in the same `apply` invocation, asking only cap-exempt questions before one final proposal, then re-renders the gate from the top with a fresh snapshot. This is one overrule pass, never a second overrule: cap-exempt questions raised during it are asked via the client's available user-input mechanism and answered before the single final proposal, and the resumed analysis steps that carry those answers are part of the same pass. During the overrule pass, a re-analysis with neither a proposal nor questions, a malformed proposal, or any non-exempt question is a lean-contract violation: STOP with zero GitHub writes and report a lean-contract violation. That inline re-analysis is a narrow exception to gathering every choice in `/plan` (see `codex-runtime`); the overrule itself performs no GitHub write, and nothing is written before a Confirm. The re-rendered gate offers only Confirm and Decline. Any answer other than Confirm counts as Decline, including freeform "Other" text. A **Decline** performs zero GitHub writes and requires no cleanup mutation: title,
body, labels, assignees, milestone, and native sub-issues are state-for-state unchanged, and
re-running refine is how to adjust. Remove the pre-confirmation parent-blocker scratch
file on Decline, per `parent-blockers.md`. Only a Confirm proceeds to the write phase below.

Once confirmed, every write proceeds in this order: claim → Working → parent body →
children+links → Pass 2 → Refined/-Working → ui:visual-check (see `### Write order`
at the end of this file). Before any of those writes, re-fetch the parent's milestone/labels
(unconditionally, even on a parent-only run) and re-verify exclusive ownership; a conflict on
the re-verify stops with zero writes, same as the pre-confirm check. Diff the re-fetched labels
against the gate-time snapshot: **authorization-sensitive drift** (`automerge:ok`, `Browser`, or
`ui:visual-check` changed on the parent) stops the run with zero writes and asks for a fresh
`$cenci:refine apply` from scratch — no in-session re-gate; **cosmetic drift** (milestone,
`area:*`, priority, team, or any other label) proceeds using the freshly fetched
snapshot and discloses the drift in the final message. For a split, also repeat
`parent-blockers.md`'s open-blocker fetch and comparison here before claiming ownership;
a read/validation failure or changed open set stops with zero writes.

**`automerge:ok` grant (apply mode, parent ticket)**: as part of the same label edit
that applies `Refined`/`Browser`, use the effective grant computed at the
Confirmation Gate above (do not recompute) — ensure the label exists (`gh label create
"automerge:ok" --repo <owner>/<repo> --color "006B75" --description "Human granted
hands-off merge at refinement — babysit may merge this PR without review" 2>/dev/null ||
true`), then append `--add-label "automerge:ok"` when the effective grant holds, or
`--remove-label "automerge:ok"` when it does not and the issue currently carries the label
(re-refine), or nothing otherwise. Every proposed split child gets its own independently
computed grant/withhold from the same gate, applied when that child is created (see below) —
never inherited from the parent.
Before the Confirmation Gate renders its manifest, when a split is proposed, first verify each child block is structurally complete — every child in the
adopted `### Suggested Split` has all six subsections present (`### Goal`, `### Size`, `### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance criteria`, `### Dependencies`), each satisfying its emptiness
rule: `### Goal` non-empty prose; `### Size` a real `<S/M> — <reasoning>` value, never empty and never the "None." sentinel; `### Dependencies` non-empty ("None." valid); `### Decisions` and
`### Assumptions (auto-adopted)` each non-empty or exactly "None."; `### Acceptance criteria` empty only
for a child the partition assigned zero criteria; a missing or empty-violating child aborts the split
before any GitHub write, before the gate renders a manifest, and before the acceptance-criteria partition check runs. This structural check only
confirms presence/absence and does not itself judge whether an empty `### Acceptance criteria` section is
legitimate — the partition check below is the sole verifier of correct assignment, and a child wrongly left
empty will surface there as an unassigned criterion. Only then verify the
proposal partitions the parent's acceptance criteria:
every parent criterion assigned to exactly one child (integration-scoped criteria on a child that
depends on all others); an unassigned or duplicated criterion aborts the split before any GitHub
write. Each child body opens with `Related to #<parent>` and, immediately after it and before any
`Parallel with #<sibling>` line, one `Depends on #<blocker>` line per effective blocker (each on
its own line; `Depends on #<sibling>` for siblings) — a **permanent, human-visible supplement** to the native `--add-blocked-by` link
applied after creation (below), never a replacement for it: `mergeDependencies` unions native and
prose sources with native state winning on any collision, and the prose line costs zero extra `gh`
calls once the native link is already applied. A child with no blockers gets no `Depends on` line
at all. Because Pass 1 creates children in dependency order, every blocker's own issue number is
known by the time its dependent is created. Inherited open parent blocker numbers come from the validated snapshot. The dependent's body file is
first written without any `Depends on` lines; that initial write omits them entirely (never a
blank placeholder), and the same body-file path is re-written with the complete effective set immediately
before that child's own `ensure-issue.sh ensure` call; a child with no blockers is written once and
never rewritten (`ensure-issue.sh`'s `bodyHash` is recorded at `init` but never read back, so this
in-place rewrite is safe). Each child body then carries its own `### Acceptance Criteria` section —
its slice of the parent's partition — after the dependency lines and description, plus that
child's own `### Size`, `### Decisions` and `### Assumptions (auto-adopted)` persisted from its
`### Suggested Split` block.

**Creation checkpoint (idempotent create/recover/repair/link, #876)**: every split child is
created through `"${PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh"`
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
the native sub-issue list carries the enumeration.

**Mark each child's blockers, immediately after its own `link` call succeeds** — one
`--add-blocked-by` per missing effective blocker after the shared `parent-blockers.md` read-before-add check, using GitHub's native issue-dependency
relationship (requires `gh` >= 2.94.0): `gh issue edit <child-number> --repo <owner>/<repo>
--add-blocked-by <blocker-number>` (several combinable via a comma-separated list or
repeated flags). Creating children in dependency order guarantees every blocker already has a
number by the time this runs. Use the same inherited-open-plus-sibling set as the body, including on the first child. Skip this entirely only for an empty effective blocker set. Verify every expected blocker is present with
`gh issue view <child-number> --repo <owner>/<repo> --json blockedBy`;
validate completeness and compare the returned numbers against the entire expected set.
A failed edit, invalid response, or missing expected link follows the same retry-once-then-stop
protocol as every other write in this procedure — do not create any further children.

If the checkpoint is missing or corrupt (bad JSON, wrong schema version), or a checkpoint field
can't be read or fails validation, on any call other than `init`, `ensure-issue.sh` itself exits
non-zero (11) and this is by design — it must **fail closed** and never silently re-create. A
candidate-derived value that can't be read or fails validation exits 12 the same way, before any write. Treat that, and any other non-zero `ensure-issue.sh` exit, as a hard
stop: report the error and do not create any further children. Once the run
completes successfully, run `ensure-issue.sh clear --checkpoint <path>` (idempotent — a second
`clear` is not an error), then perform `parent-blockers.md`'s explicit scratch-file cleanup;
an aborted run instead retains the checkpoint so the next attempt
resumes from it rather than re-creating already-created issues.

Every ticket this workflow creates — each split child — inherits the parent's milestone (as the numeric `.milestone.number`,
omitted entirely when the parent has none) and every parent label except the 10 lifecycle/transient
and refinement-granted markers (`Refined`, `Working`, `Planned`, `In Review`, `Implemented`,
`Design`, `Designed`, `automerge:ok`, `Browser`, `ui:visual-check` -- `Design`/`Designed` are
kept purely for legacy compat, so a parent's leftover label from before the design-stage
removal is never inherited onto a new child), on top of its own seed
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
argument); its standalone-`jq` surface includes `jq -n --rawfile …` payload composition at the
retitle site and `jq -e` validation/filtering in `parent-blockers.md`. Every child-ticket create now goes through
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
`ensure-issue.sh` — each child-ticket create/repair) goes through `gh api repos/<owner>/<repo>/… -X
PATCH|POST --input <json-file>` with a payload `jq`-composed from file-tool-authored raw
title/body inputs — never an inline `--title` and never a hand-escaped JSON literal.

### Write order

claim (the ownership claim, first write) → working (the Working label ensure + add) →
parent body (the parent ticket edit) → for each split child in dependency order, child-create
immediately followed by its own child-link, immediately followed by its own child-blockers when
that child has any effective blocker (inherited open parent blocker or blocking sibling).
The first child runs its blockers op too when it inherits an open parent blocker; omit the op
only for an empty effective set, and so on,
all before parent-exec-order → parent-exec-order (the Execution Order note when the split has
real ordering; unused when there is no split) → refined (the Refined label add / Working label
removal) → visual-check (the ui:visual-check label add).

Op tokens, in canonical order for a 2-child split with no open parent blockers, where child 2 depends on child 1: `claim`
`working` `parent-body` `child-create:1` `child-link:1` `child-create:2` `child-link:2`
`child-blockers:2` `parent-exec-order` `refined` `visual-check` — child 1 carries no blockers op,
because this illustration has neither inherited open blockers nor a blocking sibling for child 1.
