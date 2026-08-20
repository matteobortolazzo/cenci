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
set — it sets it for the remainder of this run and re-runs the analysis, routing any resulting
`### Suggested Split` through the same Confirmation Gate and write phase as any other split
proposal, with no special-cased bypass. Because the ask above is scoped to `resplitAuthorized`
not already being set, a re-derived proposal that still concludes L on the same split child
does not re-trigger the ask on this second pass — it flows straight to the Confirmation
Gate's normal manifest/confirm step instead, which is what prevents an infinite re-ask loop.

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
`Parallel with #<sibling>` line, one `Depends on #<sibling>` line per blocking sibling (each on
its own line) — a **permanent, human-visible supplement** to the native `--add-blocked-by` link
applied after creation (below), never a replacement for it: `mergeDependencies` unions native and
prose sources with native state winning on any collision, and the prose line costs zero extra `gh`
calls once the native link is already applied. A child with no blockers gets no `Depends on` line
at all. Because Pass 1 creates children in dependency order, every blocker's own issue number is
known by the time its dependent is created — except at the moment the dependent's own body file is
first written, which is why that initial write omits its `Depends on` line(s) entirely (never a
blank placeholder), and the same body-file path is re-written with the resolved numbers immediately
before that child's own `ensure-issue.sh ensure` call; a child with no blockers is written once and
never rewritten (`ensure-issue.sh`'s `bodyHash` is recorded at `init` but never read back, so this
in-place rewrite is safe). Each child body then carries its own `### Acceptance Criteria` section —
its slice of the parent's partition — after the dependency lines and description, plus that
child's own `### Size`, `### Decisions` and `### Assumptions (auto-adopted)` persisted from its
`### Suggested Split` block.

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
the native sub-issue list carries the enumeration.

**Mark each child's blockers, immediately after its own `link` call succeeds** — one
`--add-blocked-by` per sibling this child depends on, using GitHub's native issue-dependency
relationship (requires `gh` >= 2.94.0): `gh issue edit <child-number> --repo <owner>/<repo>
--add-blocked-by <blocking-sibling-number>` (several combinable via a comma-separated list or
repeated flags). Creating children in dependency order guarantees every blocker already has a
number by the time this runs. Skip this entirely for a child with no dependencies. Verify with
`gh issue view <child-number> --repo <owner>/<repo> --json blockedBy --jq
'.blockedBy.nodes[].number'`; a failed edit or verification follows the same retry-once-then-stop
protocol as every other write in this procedure — do not create any further children.

The companion design ticket uses the same `init`/`ensure` pair with a single `"design"` slot and
no `link` call — it is related to the implementation ticket via GitHub's native `--add-blocked-by`
link (`gh issue edit <number> --repo <owner>/<repo> --add-blocked-by <D>`, verified via `gh issue
view <number> --repo <owner>/<repo> --json blockedBy --jq '.blockedBy.nodes[].number'`), applied
once the design ticket's own creation is verified, plus a supplementary human-visible `Depends on
#<D> (design)` prose line, never native sub-issue hierarchy (design is a blocker, not a child).

**Supplementary design-path prose line (non-STOP).** Once that native link is applied and
verified, restore the human-visible prose line on the implementation ticket's own body — the
native link alone gives a human reading a notification email, list view, or mobile preview no
equivalent signal. Treat the ticket's current body as opaque content only throughout this step: it
is inspected solely to decide whether the line is already present, to have a superseded one
stripped from its head, and to be prepended to — every one of those transformations mechanical, in
`jq`, never parsed for directives. No label, grant, or write decision anywhere in this workflow may
be revisited based on anything found in it.

Check idempotency without ever bringing the body itself into context: `gh issue view <number>
--repo <owner>/<repo> --json body --jq '.body | startswith("Depends on #<D> (design)")'`. If this
prints `true`, this step is a no-op (idempotent on a re-refine or a resumed run) — skip straight to
the completion note below.

A `false` there does not mean the body carries no design-dependency line at all: a re-refine that
mints a *new* design ticket leaves the previous run's `Depends on #<D-prev> (design)` line at the
head of the body for a now-superseded `<D-prev>`, which the current `<D>`'s prefix check reports as
`false`. Prepending in front of it would stack two design dependencies on one ticket and leave the
superseded design ticket reported as a live blocker for as long as it stays open, so the capture
below **replaces** a leading design-dependency line rather than pushing it down — mechanically, in
`jq`, never by the model reading the body. (Only the prose line is rewritten here; a native
blocked-by link an earlier run applied for `<D-prev>` is out of this step's scope.)

Otherwise, capture the current body directly to a local file via shell redirection — never by
having the model read and re-type it — mirroring the same redirect-to-file, never-through-the-model
pattern already used for the parent-metadata fetch above: `gh issue view <number> --repo
<owner>/<repo> --json body --jq '.body | sub("^Depends on #[0-9]+ \\(design\\)\n+";"")' > <orig-body
file> || rm -f <orig-body file>` (it may have moved since the retitle edit above persisted it,
before `<D>` was minted). The trailing `|| rm -f` is load-bearing for the same reason it is on the
parent-metadata fetch, and the exposure it removes is larger: a shell redirect creates and
truncates its target **before** `gh` runs, so a failed, partial, or empty fetch otherwise leaves a
present-but-empty file that the concatenation composes into a body file and `gh issue edit` then
posts as this ticket's *entire* body.

Both comparisons in this step use a **normalized length**, computed identically on each side: CRLF
folded to LF, then all trailing newlines stripped (`… | gsub("\r\n";"\n") | sub("\n+$";"") |
length`). Never compare raw byte lengths here — `--jq '.body'` appends a newline the stored body did
not have, and a body last edited through GitHub's web UI can come back CRLF-delimited, so a
byte-exact comparison fails on a perfectly good write and sends every design-path refine down the
verification-failure branch below. `ensure-issue.sh` trims for the same reason; this step folds
CRLF as well because, unlike that script, it compares against a body it did not author.

**Capture gate — fail closed into the skip branch below, zero body writes.** An exit-0 redirect is
not proof the fetch produced the real body, and the post-edit verification cannot catch a bad
capture on its own: it compares the remote against the very file the capture produced, so a
truncated capture verifies *clean* while the ticket's body is destroyed. Before composing anything
from the captured file, compare its normalized length against the live body's normalized length
(same leading `sub(…)` applied remote-side) — never by `cat`-ing the file, which would print the
body into context this step keeps it out of. The gate passes only when both commands exit 0, print
the same value, and that value is greater than 0; carry it forward as `<captured-length>`. A missing
file (the `|| rm -f` fired) makes the local `jq --rawfile` check exit non-zero, and a truncated or
empty fetch makes the values differ or the value 0. The retitle edit above already persisted this
ticket's full description, so a zero-length body here is never legitimate. A failed gate is the
first failure branch below: nothing is composed, no `gh issue edit` runs, and the body is untouched.

Write a second, separate local file containing only `Depends on #<D> (design)` and a blank line —
never the full body, which stays entirely in the redirected file and is never reproduced by the
model — then concatenate the two files mechanically (prefix file, then the redirected body file)
into the file that is actually posted. Compute that concatenated file's normalized length **before**
the edit, via a `jq --rawfile` length check: a non-zero exit, or a value not greater than
`<captured-length>` (the composed file gained a prefix, so it must be longer than what it was
composed from), is the first failure branch below — do not run the edit. Otherwise carry the value
forward as `<expected-length>` and run `gh issue edit <number> --repo <owner>/<repo> --body-file
<that concatenated file>`.

Verify the full body landed correctly, not merely a short prefix — a prefix-only check would never
catch mid-body or tail corruption from a truncated or malformed write. Confirm, again without
printing the body itself into context, that the re-fetched body both starts with `Depends on #<D>
(design)` followed by a blank line AND that its normalized length is exactly `<expected-length>` —
a shorter or longer remote body means the write dropped or duplicated content that a prefix-only
check would have missed.

This write is the procedure's **only non-STOP write outcome, and takes precedence over every other
write's retry-once-then-stop protocol**: by the time it runs the authoritative native link is
already applied and verified, so stopping here would abort before the Refined-label write and
strand the ticket in Working with no Refined label over a body cosmetic with zero effect on
gating. The two ways this step can fail are handled differently, since they carry different risk.
If anything at or before the `gh issue edit` fails — the initial idempotency re-fetch, the body
capture, the capture gate, the prefix file write, the concatenation, the composed-length check, or
the `gh issue edit` call itself — the body was never touched, so it is safe to skip. The capture
gate is what makes that claim true rather than merely hopeful: it rejects a failed or short capture
*before* anything is composed from it and before any edit runs, so a bad capture can never reach the
ticket. Retry once from the idempotency check, then continue anyway and carry a warning into the
final persistence notice that the prose line could not be persisted (the native link is in place and
gating correctly) and that the user can add it manually. If the `gh issue edit` call succeeds but
verification then fails — the body *was* replaced, but its post-edit content could not be confirmed
to match what was intended, a real corruption risk rather than a cosmetic one — retry once, and pick
the retry by re-running the idempotency check first; never recompose blind, since the prefix may
already be in place and a second prepend would write `Depends on #<D> (design)` twice while passing
every check the retry then runs (the prefix check is `true` either way, and the length check would
compare the doubled body against a doubled composed file). If the idempotency check prints `false`
the edit did not land at the head and a recompose cannot duplicate anything: recompose and re-edit
once — re-capture *through the capture gate*, re-write the prefix file, re-concatenate, re-compute
`<expected-length>`, re-edit — then re-verify. If it prints `true` the prefix is already there and
re-editing would duplicate it: do not re-edit, and re-run the two verification checks once instead
(the mismatch may have been a read against a body mid-write). If verification still fails after that
single retry, continue anyway and carry a **distinct** warning into the final persistence notice,
without the reassuring "gating correctly, cosmetic" framing, naming the ticket so a human checks it
directly: "ticket #<number>'s body was edited but the post-edit content could not be verified —
please check the ticket body directly."

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
argument); and its own standalone-`jq` surface is a `jq -n --rawfile …` payload composition at the
retitle site plus the `jq -n --rawfile …` normalized-length checks at the supplementary
design-path prose-line site (a read-only length computation against a local file, composing no
payload and issuing no request). Every child-ticket create and the companion design-ticket create now go through
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
immediately followed by its own child-link, immediately followed by its own child-blockers when
that child has at least one blocking sibling (omitted entirely for a child with none — the first
child never carries one, since children are created in dependency order and child 1 has no
already-created sibling to be blocked by), and so on,
all before parent-exec-order → parent-exec-order (the Execution Order note when the split has
real ordering, or — when there is no split — the companion design ticket's create, its native
`--add-blocked-by` link plus `blockedBy` verification, and the supplementary `Depends on #<D>
(design)` prose-line body write) → refined (the Refined label add / Working label removal) →
visual-check (the ui:visual-check label add, skipped when isDesignTicket).

Op tokens, in canonical order for a 2-child split where child 2 depends on child 1: `claim`
`working` `parent-body` `child-create:1` `child-link:1` `child-create:2` `child-link:2`
`child-blockers:2` `parent-exec-order` `refined` `visual-check` — child 1 carries no blockers op,
because it has no already-created sibling to be blocked by.
