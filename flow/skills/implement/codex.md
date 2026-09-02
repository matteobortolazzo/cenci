# Codex implement procedure

Read `project-core` and `codex-runtime`. In `/plan`, gather context with the read-heavy
agent and ask material questions.
An open native blocking dependency is a hard stop before any mutation: check the ticket's `blockedBy` (`gh issue view <ticket> --json blockedBy`) and, if any entry is `OPEN` or otherwise unresolvable, stop immediately, naming every blocking ref and its state (a `gh` that rejects the field is a capability gap — that case warns once and proceeds instead) — no plan persisted, no ticket claim, no worktree; re-run once the blockers close. `apply` runs the identical check again as its own first step, before it persists the plan file, initializes the checkpoint, creates the worktree, or writes any label: a blocker linked after `/plan` returned would otherwise sail straight into every mutation this gate exists to prevent, and an approved plan is not evidence the dependency is still clear. On that path report whatever assignee and labels an earlier run already left on the ticket rather than claiming the run stopped before the ticket was claimed, and say that nothing later in the run will reconcile them.
Split-child provenance for this gate is derived the same way `skills/refine/codex.md`'s
Split-depth guard derives it: `gh issue view <ticket> --repo <owner>/<repo> --json parent --jq '.parent.number // empty'` (a returned number means this ticket is a split child of
that parent, giving `isChild`/`parentId`), falling back to a `Related to #<number>` first
non-empty body line for older convention-linked tickets or a non-zero primary command —
run once during context gathering, before the Split Gate is ever evaluated.
When the planner's output carries a non-empty
`### Split Recommendation` or a `### Size Estimate` of `L`, the Split Gate asks, via
the client's available user-input mechanism, whether to stop — split via
`/cenci:refine`, persisting nothing — or proceed as a single PR; only Proceed
continues planning. When the ticket is itself a split child (`isChild` true), the stop
option instead points at the **parent** — `/cenci:refine <parentId>`, never the child's
own `/cenci:refine <id>`, which is a dead end under the refine skill's split-depth guard.
Two call sites — this interactive/ticketless Stop outcome, and the equivalent lean-mode
unattended escalation for that same split child (fired unconditionally, before any human
has answered) — additionally post a best-effort, non-blocking evidence comment (the
planner's `### Size Estimate`/`### Split Recommendation`, verbatim, marked
`<!-- cenci-oversize-child -->`) to the parent ticket; a failure to post is reported but
never blocks either outcome, and this write never happens for a non-child ticket
(`skills/implement/phases/phase-1-plan.md`'s `### Split Gate` is authoritative for the
exact procedure). One run persists at most one plan file and opens at most one PR:
never persist a second plan file or open a second or stacked PR for one ticket.
Then return an approved plan in the conversation without writing files or labels.
Stop before mutations and instruct `cenci run implement apply <ticket> --agent codex`.
This procedure has no autonomous approval path: `/plan` always stops before
mutations and returns the plan in the conversation for a human to approve, so
every plan it persists is `approval: human` by construction and needs no
value branch of its own (`skills/implement/phases/phase-1-plan.md`'s
`## Persist the Plan` documents the key and the three autonomous values the
Claude Code paths write). If this procedure ever gains a path that persists a
plan without that stop, it must write the matching value instead.
In normal apply mode persist `.plans/<ticket>-<slug>.md`, then verify the
assembled file contains all three required headings (`## Ticket Details`, `## Implementation
Plan`, `## Architectural Context` — the `requiredPlanSections` list in
`watch/internal/pipeline/planfile.go`): if any is missing, stop before the checkpoint/label
mutations and report the missing heading(s). Otherwise,
initialize the checkpoint, create the worktree, then run the baseline gate: resolve affected projects
by matching the plan's `## Project Context` `# Project: <name>` headers against
`.cenci/config.json`'s `projects[].name` to get each `slug`. When the primary signal
under-matches, fall back to the `### Technical Notes` "Affected services"/"Affected components" lines inside `## Ticket Details`, matched against each `projects[]` entry's `slug`, `name`, or `path`
(dedupe; a no-match prints a
one-line notice and no-ops, while an unset gate is a fully silent no-op), and for each
resolved target run `( cd "<abs-worktree-path>" && sh "${PLUGIN_ROOT}/hooks/scripts/run-gate.sh" "<slug>" )`.
`GATE_STATUS=green` or `GATE_STATUS=unset` → proceed.
`GATE_STATUS=red` → stop as **gate failed**, capturing the resolved slug (or "top-level") and
the command's output; a non-zero exit with no `GATE_STATUS=` line → stop as **gate could not
run** (a script/config error, e.g. missing `jq`, malformed config, bad project path), capturing
the same slug and output. Either stop: run `checkpoint.mjs block`, clear the goal, retain the worktree and branch, report
the distinct diagnostic plus its captured output, and tell the user to fix the baseline gate
and re-run `cenci run implement apply`.
The captured output above shows at most `cenci.gateOutputLines` trailing lines (default 120) of
the gate command's combined stdout+stderr; a red gate's output also carries a `GATE_LOG=<absolute
path>` line where the full untruncated output is retrievable — locate the failing detail there
with the client's search tool rather than assuming the truncated tail already contains everything.
Arm
the native goal and implement test-first. Then, before the reviews, run the reuse check: for each
named unit this change *adds* (helper, constant, fixture — additions only, capped at the 10
largest, searched by name fragment and by a distinctive body line — two searches per unit, at most
20 for the whole check — within the affected project's directory only, derived from this change's
own file list rather than any earlier stage's state, and skipped outright when the change adds none),
reuse it rather than re-implementing it when an equivalent already exists,
consolidating even at two occurrences since one of them is being written
right now; keep the new code and report the
near-duplicate when the existing unit cannot be reused without changing behavior for its
current callers. This is bounded to new code — never a repo-wide duplication sweep.
Any consolidation made here is covered by the same full-suite-and-lint run as the rest of the
phase — re-run both before proceeding — and a reported near-duplicate is recorded for review
visibility only, never tracked or turned into a Followup ticket.
Then run the configured reviews, fix accepted findings,
capture lessons, and run the maintenance check
(`flow/skills/maintain/scripts/check.sh --changed`) when the change touches docs, skills,
agents, config, or client adapters, with the shell tool working directory set to the verified absolute worktree path on the initial check, every `--write`, and every re-run.
Core checking remains active regardless of `maintenance.enabled`;
`maintenance.checkDuringImplement=false` makes all findings report-only (including any `fail`,
which is that setting's deliberate opt-out from the gate below), and
`maintenance.generatedDocs=false` disables generated-section maintenance. Otherwise,
auto-repair only drift caused by this same change and route any ambiguous or policy-affecting
finding through the client's available user-input mechanism.
A checker `fail` is CI-blocking: gate on it through the client's available user-input mechanism
with a fix / stop / explicit push-anyway choice, and never push an unresolved `fail` —
CI runs the identical `--changed` invocation, so shipping one is a guaranteed red pipeline.
`warn` and `skip` stay advisory. On a push-anyway override, name the accepted failure in the PR
body and never render it as a passing check. Then commit, push, and open the PR. Clear the goal
before any question/error and after PR creation. After a successful PR creation (never on a
failed `gh pr create`), archive the consumed plan file instead of deleting it. `.plans/` lives
only in the main checkout (repo root), not in the worktree, so anchor the command to
`<repo-root>` (the main checkout containing `.worktrees/` — resolve via `git -C <worktree-path>
rev-parse --path-format=absolute --git-common-dir` if needed, not the worktree itself). A single
`&&`-chained guard cannot distinguish "no plan file" from "guard true but mkdir/mv genuinely
failed" (both exit non-zero), so use an if/else that emits a distinct marker per outcome: `if [ -f
"<repo-root>/.plans/<filename>" ]; then mkdir -p "<repo-root>/.plans/done" && mv -n
"<repo-root>/.plans/<filename>" "<repo-root>/.plans/done/" && echo ARCHIVE_OK || echo
ARCHIVE_FAILED; else echo ARCHIVE_SKIPPED; fi` (a plain `mv`, no git — `.plans/` is
untracked/gitignored; `mv -n` no-clobber intentionally leaves a pre-existing same-named
`.plans/done/<filename>` in place on a re-implementation rather than overwriting the earlier
archived record, and still reports `ARCHIVE_OK` since the skip itself is not an error). Key the
final session summary off the marker, not the exit code: report `ARCHIVE_FAILED` (a real
failure — permissions, disk full, cross-device); `ARCHIVE_SKIPPED` (no plan file for this run)
and `ARCHIVE_OK` need no special reporting. On every exit from this step, success or
failure,
stop every background shell this run started that is still running
— an abandoned background process outlives the session, and on clients that report
in-flight shells at turn end it keeps cenci-watch holding the session at `running`
instead of `done` (#698/#699), which nothing later clears. The babysit launch below is
exempt: it detaches its own supervisor, so its launching shell exits normally. Then,
as the final step once the goal is
cleared, hand the open PR to the persistent supervisor so it carries the PR to merge and does
the final `In Review` → `Implemented` relabel: resolve the watch interval from
`.cenci/config.json`'s optional `babysitInterval` via
`${PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh` (top-level, or the affected
project's slug), then launch `cenci babysit <pr> --agent codex --interval <interval>` — omit
`--interval` when the resolver prints nothing, letting the CLI use its built-in `15m` default.
This launch is non-blocking (the supervisor detaches and survives session exit) and
best-effort: treat `supervisor already running for PR #<pr>` as expected success (a re-entry
already armed it), and report any other launch failure without failing the run — the open PR
is the real deliverable. Never force-push or bypass
security/design/approval gates.
