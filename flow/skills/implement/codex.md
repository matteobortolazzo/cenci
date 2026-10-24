# Codex implement procedure

Read `timing.md` and apply its local timing procedure from worktree setup through the final summary. Timing is advisory and never changes a gate.

Apply the plugin's `docs/model-escalation.md` at each applicable boundary: before
presenting fresh/resumed planning output, after two failed attempts at an unexplained
implementation defect, and before accepting security/silent-failure reviews. When
triggered, the root delegates a bounded second opinion to `domain-specialist` and
feeds validated advice back to the original worker. Preserve all existing approval,
autonomy, retry, stop, and review-evidence rules; routine phases keep their models.

Read `project-core` and `codex-runtime`. In `/plan`, gather context with the read-heavy
agent. Before delegating the planner, use `frontend-classification` on the ticket/task
title and requirements to resolve `isUiTicket` and pass that flag to the planner; an
unreadable classification input stops planning for repair rather than defaulting false.
Also before delegating the planner, discover the repository's documentation conventions: run `"${PLUGIN_ROOT}/skills/implement/scripts/doc-conventions.sh" discover "<abs-repo-root>"` (read-only; prints paths and classification lines only; redirect stderr to a separate capture) and pass its stdout verbatim to the planner as a `Documentation discovery:` block. Any non-zero exit, or empty stdout, means discovery is unavailable: pass `Documentation discovery: unavailable (<reason>)`, never a hard stop. The planner never runs the script; it fills the plan's `### Documentation Routing` section from the block, and a re-plan after a stale or unknown draft re-runs discovery.
When `Documentation discovery:` reports `convention: none`, present its `recommend:` block as a recommendation under `### Documentation Routing`, never as a migration step in `### Files to Modify`.
Apply `agents/planner.md`'s shared question selection in interactive mode: resolve entailed and repo-grounded choices, record them as `auto-adopted:` with rationale (and the main alternative for judgment calls), and ask only unresolved or consequential questions through the client's available user-input mechanism. Allow zero questions; never fill a quota or reopen settled answers. Preserve the planner's security, irreversibility, scope, and acceptance-criterion stop rules. These resolutions do not post GitHub decision comments or change `approval: human`.
An open native blocking dependency is a hard stop before any mutation: check the ticket's `blockedBy` (`gh issue view <ticket> --json blockedBy`) and, if any entry is `OPEN` or otherwise unresolvable, stop immediately, naming every blocking ref and its state (a `gh` that rejects the field is a capability gap — that case warns once and proceeds instead) — no plan persisted, no ticket claim, no worktree; re-run once the blockers close. `apply` runs the identical check again as its own first step, before it persists the plan file, initializes the checkpoint, creates the worktree, or writes any label: a blocker linked after `/plan` returned would otherwise sail straight into every mutation this gate exists to prevent, and an approved plan is not evidence the dependency is still clear. On that path report whatever assignee and labels an earlier run already left on the ticket rather than claiming the run stopped before the ticket was claimed, and say that nothing later in the run will reconcile them.
Split-child provenance for this gate is derived the same way `skills/refine/codex.md`'s
Split-depth guard derives it: `gh issue view <ticket> --repo <owner>/<repo> --json parent --jq '.parent.number // empty'` (a returned number means this ticket is a split child of
that parent, giving `isChild`/`parentId`), falling back to a `Related to #<number>` first
non-empty body line for older convention-linked tickets or a non-zero primary command —
run once during context gathering, before the Split Gate is ever evaluated.
When the planner's output carries a non-empty, non-`None`
`### Split Recommendation` (`None` here means exactly `None`/`None.` or a single-line
well-formed `None — <rationale>`, never a prefix match) or a `### Size Estimate` of `L`, the Split Gate asks, via
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
The plan also carries a `### Size Check` — the plan's files and estimated changed lines
compared against the ticket's `### Size Estimate: <letter>` bound from `docs/ticket-sizing.md`
— the confirmed `### Size Estimate: <letter>` (bound line verbatim, rationale), and a
`### Delegation Preflight` in which every lane is at most one M and an L ticket is always
several lanes with exclusive file ownership. During `/plan`, use `agents/planner.md`'s
complete Delegation Preflight schema (owned files, verification, Scope, Wave and Depends on
for every delegated lane), required `### Parallelism Assessment` (parallel or a concrete
sequential reason), and eligible `### Parallel Lanes` declaration. Explicitly consider a
shared foundation wave before independent consumers; never omit the assessment on a fresh
plan. A `### Size Check` verdict of `exceeds` (growth
during planning), or a single-lane L, is a hard stop with no proceed option in any mode:
return the planner's split proposal, persist nothing, and point at `/cenci:refine` (the parent
for a split child) — model capability never raises the verdict. Every implementer delegation
in `apply` is briefed verbatim that its working budget is about half of its model's advertised
context window, that automatic compaction is disabled, and that a low-context state is reported
as `Context: low — done: …; remaining: …`; a worker reporting that line, or a lane needing a
second resume, is reported to the root as a split signal and stops the run — never retried
with a longer lane.
In ticket mode, `/plan` also runs the read-only `cenci pipeline plan-check <id>` (ticketless mode skips it); when its `pipeline.worktreePath` is non-empty, append `Leftover worktree <path> (branch <branch>) — Phase 2 fails with ErrWorktreeExists until a human removes it` to the planner's context, so the new plan records it under `### Risks`.
If that `plan-check` fails, add `plan-check unavailable: <error>` to the planner's context under `### Risks` and continue: `/plan` is read-only and never stops on it.
`/plan` makes no `cenci pipeline` call that writes anything.
Then return an approved plan in the conversation without writing files or labels.
Stop before mutations and tell the user to continue with `apply` in this same session (a new-plan apply); `cenci run implement apply <ticket> --agent codex` is only for resuming an already-saved plan after a crash.
This procedure has no autonomous approval path: `/plan` always stops before
mutations and returns the plan in the conversation for a human to approve, so
every plan it persists is `approval: human` by construction and needs no
value branch of its own (`skills/implement/phases/phase-1-plan.md`'s
`## Persist the Plan` documents the key and the four autonomous values the
Claude Code paths write). If this procedure ever gains a path that persists a
plan without that stop, it must write the matching value instead.

**Resume routing (apply, ticket mode).**
Ticketless (task) mode skips this block, every `cenci pipeline` call below and all resume routing.
In ticket mode, right after the blocked-dependency check and before any plan-file write, checkpoint write, worktree call or label write, run the apply-time `plan-check`: run `cenci pipeline plan-check <id>` for a saved-plan apply (`apply <ticket>`, `resume <ticket>` or a `.plans/<file>` target), or `cenci pipeline plan-check <id> --replan-requested` for a new-plan apply (an `apply` in the same session as an approved `/plan` result).
Read `stage`, `worktreePath`, `branch`, `prUrl` and `worktreeRegistered` only from that one result's `pipeline` object, which the CLI echoes for every decision; never read `.cenci/pipeline/` yourself and never make a second `plan-check` call. Each stage is recorded when its phase starts, so a crashed run's stage names the phase it entered.
`plan-check`'s own `errors[]` for the `none` and `multiple` decisions is a normal branch (exit 0), not the lifecycle hard-stop below.

| `pipeline.stage` | Route |
|---|---|
| `new`, `prepared`, `waiting_for_input`, `waiting_for_plan_approval` | no resume routing |
| `plan_approved`, `executed` | attach, then Implementation dispatch with the resume brief |
| `reviewed` | attach, then review entry |
| `finalized` with no `prUrl` | attach, then review entry |
| `finalized` with `prUrl` | stop |
| any other value | unknown-stage stop |

- `resume`: check the two stops below, then take the table's route; no question is asked.
- `stale`: check the two stops below first, then ask Continue or Re-plan through the client's available user-input mechanism. Continue takes the table's route (a stage with no attach route takes the normal flow: `--slug` and the standard brief). Re-plan ends the run and tells the user to run `/plan` again, with no `cenci pipeline reset` at that point.
- `multiple` on a saved-plan apply: ask which file to use, or whether to re-plan, through the client's available user-input mechanism. A picked file is handled exactly like `stale`; re-plan ends the run like the `stale` Re-plan choice.
- `multiple` on a new-plan apply: stop before any mutation, name every file in `artifacts[]`, and ask the user to remove the outdated ones and re-run the apply. The `multiple` prompt above applies only to a saved-plan apply.
- `awaiting-input`: stop before any mutation, print that the saved plan is an unanswered draft, and point the user to answering on the ticket and resuming with Claude `/cenci:implement <id>`, or re-running `/plan`.
- `none` on a saved-plan apply: stop and tell the user to run `/plan`; never improvise a plan.
- An empty or unrecognized decision (exit 1): show `errors[0]` and stop before any mutation.
- A new-plan apply (decision `none` or `replan`) whose `pipeline.worktreePath` is non-empty: run `cenci pipeline reset <id>` then `cenci pipeline prepare <id>`, then persist the new plan. A failure of that `reset` or `prepare` (a non-empty `errors[]`, a non-zero exit, or empty or unparseable output) stops the run before the persist step and reports it. The worktree and branch on disk are left alone, and a new-plan apply never takes a resume route.
- Attach routes apply only when `pipeline.worktreeRegistered` is true; when it is false no resume route is taken and the worktree step follows its gone/leftover handling.
- PR stop: for `finalized` with a `prUrl`, print `#<id> already has PR <prUrl> — nothing to resume` and end the run, noting that the plan-file archive and the `cenci babysit` arm may not have run for that PR.
- Unknown-stage stop: for any other value, print `Unknown pipeline stage <stage> for #<id> — not resuming`, then every `warnings[]` entry, and end the run; a missing or unreadable stage renders as `(missing)`.
- Both stops run before the persist step and touch nothing: no transition, no checkpoint, no worktree call and no label write.
- The crashed run's uncommitted changes are kept: the root agent and every worker never stash, reset, check out over or discard them.

**Pipeline lifecycle (ticket mode).**
In ticket mode apply records this `cenci pipeline` lifecycle, in this order, each call at the step that names it below:
1. `cenci pipeline prepare <id>`
2. `cenci pipeline plan <id>`
3. `cenci pipeline plan <id> --approve`
4. `cenci pipeline artifact <id> --plan <plan-path>` (the `.plans/<id>-<slug>.md` just persisted on a new-plan apply, or the saved plan file from `artifacts[0]` of the `plan-check` result (the picked file after a `multiple` choice) on a saved-plan apply)
5. `cenci pipeline worktree <id> --slug <slug>` (`--attach <worktreePath>` on an attach route)
6. `cenci pipeline execute <id>`
7. `cenci pipeline review <id>` (at acceptance-review entry)
8. `cenci pipeline finalize <id>` (before the rebase/commit step)
9. `cenci pipeline artifact <id> --pr <pr-url> --pr-number <n>` (only after a successful `gh pr create`)
Every one of these calls stops the run on a non-empty `errors[]` and reports the errors.
A call that exits non-zero, or prints empty or unparseable output, stops the run the same way and reports its stderr; this covers the re-plan `cenci pipeline reset <id>` too.
On a resume the monotonic `plan --approve` and `execute` calls run unchanged and return no-op `warnings[]`; show them in the status update and never treat them as failures.

In normal apply mode persist `.plans/<ticket>-<slug>.md` (a new-plan apply only: a saved-plan apply keeps the file `plan-check` already validated and skips this persist and its heading check), then verify the
assembled file contains all three required headings (`## Ticket Details`, `## Implementation
Plan`, `## Architectural Context` — the `requiredPlanSections` list in
`watch/internal/pipeline/planfile.go`): if any is missing, stop before the checkpoint/label
mutations and report the missing heading(s). Otherwise, in ticket mode run `cenci pipeline prepare <id>`, `cenci pipeline plan <id>`, `cenci pipeline plan <id> --approve` and `cenci pipeline artifact <id> --plan <plan-path>` in that order, where `<plan-path>` is the `.plans/<id>-<slug>.md` just persisted on a new-plan apply, or the saved plan file from `artifacts[0]` of the `plan-check` result (the picked file after a `multiple` choice) on a saved-plan apply.
Then initialize the checkpoint: `checkpoint.mjs advance` when a checkpoint file already exists on an attach route, never `init` (it overwrites unconditionally and would drop the recorded `worktree` and `planPath`), and `checkpoint.mjs init` otherwise.
Then create the worktree: in ticket mode run `cenci pipeline worktree <id> --slug <slug>` (the `.worktrees/<id>-<slug>` naming), or `cenci pipeline worktree <id> --attach <worktreePath>` instead of `--slug` on an attach route, which applies only when `pipeline.worktreeRegistered` is true; ticketless mode creates it with git directly under `.worktrees/`.
On an attach route print exactly one line after attaching: `Resuming #<id> from stage <stage> in <worktreePath> (branch <branch>, <n> commits ahead of main, <m> uncommitted files)`, with `<n>` from `git -C <worktreePath> rev-list --count main..HEAD` and `<m>` from the line count of `git -C <worktreePath> status --short`; if either count command fails (for example there is no local `main`), stop the resume with the git error and never guess a count.
When `pipeline.worktreePath` is non-empty but `pipeline.worktreeRegistered` is false no attach route is taken: if the directory is gone, print `Recorded worktree <worktreePath> is gone — creating a fresh one`, run `--slug` and use no resume brief; if the directory still exists, run `--slug` and, on `ErrWorktreeExists` (a leftover directory, or the recorded branch still existing locally), stop naming that directory or branch and never remove the directory or delete the branch.
The crashed run's uncommitted changes are never stashed, reset, checked out over or discarded by the root agent or any worker.
Then run `cenci pipeline execute <id>` (ticket mode), before the baseline gate.
On an attach route run the baseline gate only when `git -C <worktreePath> status --short` is empty; otherwise print `Baseline gate: skipped — resumed worktree has <m> uncommitted files from the crashed run` and proceed.
Then run the baseline gate: resolve affected projects
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
the native goal and implement test-first using the execution procedure below.

**Implementation dispatch (apply only).** Resolve `isUiTicket` using the `frontend-classification`
reference skill against the ticket/task title and requirements in the persisted plan;
set it explicitly true or false, as during planning. Recompute it on every apply, including
resumed and legacy plans; an unreadable or incomplete classification input stops dispatch
for repair, never defaults to false. Use this resolved flag for both fan-out eligibility
and the visual/screenshot checks below.
Re-read the persisted `### Delegation Preflight`
before any implementation mutation and enforce Phase 3's `## Delegation Preflight Re-check`
and `## Context Budget`, including malformed ownership, lane size and split-signal stops.
Stop at the same return-to-planning gate (`replan`) when a delegated lane's Verification cell is empty
or is a `full:` fallback with no reason; a legacy plain command is treated as a scoped command.
Read `### Parallelism Assessment`; a missing legacy assessment means sequential execution
with that reason reported, never inferred fan-out. Use native `implementer` subagents,
passing the worktree, plan sections, owned files, the lane's resolved scoped Verification command (its
Verification cell, which is the only source of `Tests:`), acceptance criteria and context-budget brief
to each. Workers run that scoped Verification command during red/green work and never the full suite,
and report with these literal formats: `Verification (scoped): <command> — <pass|fail> — not covered: <scope outside the command>`,
`Verification (full fallback): <command> — <pass|fail> — reason: <reason>` for a `full:` fallback lane, and
`Verification (full): <command> — <pass|fail>` for the Completed-Change Verification run.
A scoped pass is never reported as full suite, all tests or CI green.
Workers are not alone in the worktree and must preserve
other workers' changes; the root alone owns shared git state and external writes.
Always forward the plan's `### Documentation Routing` section verbatim to every `implementer` and `critical-reviewer` delegation (including `lite-small`'s single reviewer) and to the lessons collector.

On an attach route, first read `git -C <abs-worktree-path> status --short` and `git -C <abs-worktree-path> log --oneline main..HEAD`. With 0 commits ahead of main and 0 uncommitted files, print `Resumed worktree is clean — running implementation with the standard brief` and run this dispatch with the standard brief only, on every attach route (this replaces the review-entry route for `reviewed` and `finalized` without a `prUrl`).
If any git read in this block (`status --short`, `log --oneline main..HEAD` or `diff --stat HEAD`) fails, stop with the git error: never treat missing output as a clean worktree and never paste git error text into the brief.
A resumed worktree is clean only with 0 commits ahead of main and 0 uncommitted files; otherwise (any commit ahead of main or any uncommitted file) it is dirty.
On the `reviewed` and `finalized`-without-`prUrl` routes with a dirty worktree, skip this Implementation dispatch, the lane verification barrier, the refactor pass, the reuse check and Visual Verification, and continue at the acceptance review.
That resume runs Completed-Change Verification only when a fix delegation in that session changes the worktree; otherwise Phase 9's post-rebase full run is the full check.
On the `plan_approved` and `executed` routes with a dirty worktree, every implementer delegation receives the resume brief: the single implementer, every parallel or sequential lane and the compact implementer. Gather `git -C <abs-worktree-path> status --short`, `git -C <abs-worktree-path> diff --stat HEAD` and `git -C <abs-worktree-path> log --oneline main..HEAD`, and copy this block into each delegation byte-for-byte, filled with those three outputs:

```text
Resume brief — an earlier run of this ticket crashed partway through implementation
and left this worktree as it is. Nothing is committed unless it appears in the log below.

git status --short:
<output>

git diff --stat HEAD:
<output>

git log --oneline main..HEAD:
<output>

Before implementing any plan task, check which plan tasks are already done in the
worktree (the files above and the tests that already exist) and continue from what
exists; do not redo finished work. Never stash, reset, checkout over or discard these
changes, and do not stage or commit them.
```

On the `plan_approved` and `executed` routes, after re-running completed lanes' scoped tests, the run then always runs Completed-Change Verification once Phase 5's cleanup is done; evidence is never persisted across sessions, so a resume never reuses an earlier full run.

A crash-resume brief is not a second resume for the context-budget split signal: that count is per session, and the crash was a memory kill, not context exhaustion. Tests that already exist and pass because the implementation is present are expected on a resume, not a red-phase violation; tasks not yet done follow the normal red-before-green order.

With exactly one delegated lane, `cenci.compactImplementation` exactly true (default false)
permits a small S, low-risk, concrete plan to combine red/green/refactor in one worker.
Follow Phase 3's `## Compact Implementation`, including its exclusions, observed red-before-green
evidence and bounded Reuse Check. Its full build, test and lint run is its Completed-Change Verification,
with no other full group before acceptance review. Otherwise use the ordinary
separate test, implementation and refactor delegations.
In ordinary mode the refactor delegation ends with Phase 5's `## Completed-Change Verification` (`phases/phase-5-refactor.md`), before the acceptance review.
Compact mode never merges lanes.

With multiple lanes, Read Phase 3’s `## Parallel Lanes` and execute its structural re-check,
eligibility re-check, bounded dispatch, dependency waves, worker rules, error gate and resume
procedure using native subagents. Consume `Wave:` and `Depends on:` from the preflight; a
shared foundation finishes first and its files stay read-only for later consumers. Apply
`isUiTicket`, sensitive-path, auth/payment/migration and test-resource restrictions before
fan-out. Disqualification runs one worker per lane sequentially in wave order, never one
worker for the entire plan. Resolve `cenci.implementerConcurrency` (default parallel) and
`cenci.implementerMaxConcurrency` (positive integer, default 2); use Phase 3's invalid-value
fallbacks and cap launches to available worker slots. Its scheduler consumes that cap;
never launch all lanes together merely because the mode is parallel. Every lane must
report scoped red-before-green evidence, stay within ownership and avoid full-suite runs.
Failed prerequisites stop queued lanes and later waves, preserve completed edits and take
the shared error gate; no barrier or reviews while any lane is failed or incomplete.
After all waves finish, use one refactor pass with the reuse check below. The Lane Verification Barrier
runs at Completed-Change Verification, after the refactor pass: delegate one fresh worker for Phase 5's
`## Completed-Change Verification` (full build, test and configured lint, integration fixes, exact results,
the same three-attempt limit and context/diagnosis stops).
A delegation report without a `Verification (full): <command> — pass` line means Completed-Change Verification did not run: rerun it, counting an attempt toward the three only when it ran and failed.
In every mode run Phase 4’s `## Visual Verification` and screenshot persistence for UI
tickets before acceptance review; sequential UI lanes and compact work keep that evidence.
All existing acceptance/code reviews, approval rules and one-PR delivery still apply.

Then, before the reviews, run the reuse check (inside the compact delegation when selected): for each
named unit this change *adds* (helper, constant, fixture — additions only, capped at the 10
largest, searched by name fragment and by a distinctive body line — two searches per unit, at most
20 for the whole check — within the affected project's directory only, derived from this change's
own file list rather than any earlier stage's state, and skipped outright when the change adds none),
reuse it rather than re-implementing it when an equivalent already exists,
consolidating even at two occurrences since one of them is being written
right now; keep the new code and report the
near-duplicate when the existing unit cannot be reused without changing behavior for its
current callers. This is bounded to new code — never a repo-wide duplication sweep.
Any consolidation made here is covered by the same Completed-Change Verification run as the rest of the
phase — it runs once, after this check, never before it — and a reported near-duplicate is recorded for review
visibility only, never tracked or turned into a Followup ticket.
Then, before any code review, run the acceptance review: at its entry, in ticket mode, run `cenci pipeline review <id>`, then delegate the `acceptance-reviewer`
role with the ticket body, its design record (the persisted plan) and the diff — never the
diff alone — and persist its `## Acceptance Review` table; compute the verdict with
`"${PLUGIN_ROOT}/skills/review/scripts/acceptance-verdict.sh" <table> [--ui-local]`
(`--ui-local` for a UI ticket, which requires gallery and screen captures in en-US and it-IT; a
diff-only review of a UI ticket fails). A partial or missing item returns the run to
implementation (at most two rounds, then stop and ask); only a pass reaches the code reviews.
Then run the configured reviews. A reviewer's finding is a claim, not evidence — per Anthropic's
Opus 5.5 guidance for subagent fan-out, when a subagent reports back, check its evidence before you accept it:
before delegating or applying any fix for a Must Fix, Critical, or High finding, read the cited
file and line yourself, and accept it only once that read confirms the finding holds against the actual code.
A finding that does not hold is not fixed but recorded as a one-line `Considered and discarded`
entry in the PR's `## Notes` (what was claimed, why it does not hold). Triage every lower-tier finding (Medium/Low, Should Fix, non-critical Warning) once, here: fix now if the fix is straightforward.
Otherwise, discard by default — test-coverage gaps, doc polish, "consider X" suggestions, and refactor/tech-debt observations are never tracked, only recorded as a one-line `Considered and discarded` entry in the PR's `## Notes`, which never becomes a Followup ticket.
**Track** it only when it is an actual defect with a concrete, realistic trigger path a user can hit in real use by noting it in the PR's `## Notes` for the Followup Ticket step, which records the tracked items through `cenci pipeline followup --source implement`.
Each acceptance-fix and review-fix delegation runs its fix's scoped Verification command, and a fix report that lists a changed file marks the Completed-Change Verification evidence stale;
a report that lists none leaves it valid, and an ambiguous or missing report counts as stale.
Every fix-delegation report must carry a literal `Changed files:` line (a file list, or `Changed files: none`); a report without that line, and any edit the root agent applies itself, counts as stale.
When the evidence is stale, run Completed-Change Verification once, after the last fix round
(never once per round), before any documentation edit; a `reviewed` resume without mutation runs none, because Phase 9's post-rebase full run covers it.
Fix the accepted findings,
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
body and never render it as a passing check.

Documentation policy: before capturing lessons or editing any documentation, resolve it with `"${PLUGIN_ROOT}/skills/implement/scripts/docs-policy.sh" "<abs-worktree-path>"` (read-only; prints `minimal` or `default`; an unknown `docs.policy` value resolves to `default` with one warning naming the key; exit 2 means apply default behavior and report `docs: error (policy unresolved)`).
Redirect the resolver's stderr to a separate capture and read the policy token from stdout only; treat anything other than exit 0 with stdout exactly `minimal` or `default` (exit 2, exit 126/127, empty or unexpected stdout) as unresolved: apply default behavior and report `docs: error (policy unresolved)`.
In `minimal` mode, a run with no plan-named doc file, no `docs-to-update.txt` entry (no `Docs to update:` item the implementer reported), no changed reading trigger (a doc an `AGENTS.md`/`CLAUDE.md` read-when pointer routes to was added, renamed or removed, or such a pointer became inaccurate) and no admitted lesson (a non-obvious failure or rework a written lesson could prevent, not already covered by a doc) makes zero documentation edits, skips the `AGENTS.md`/`CLAUDE.md`, `README.md` and topic-doc update steps, and does not launch `lessons-collector`.
In `minimal` mode, evaluate the `docs-to-update.txt` entry condition by reading `docs-to-update.txt` itself, never from memory: an absent file means no entry, and a file that exists but cannot be read records `docs: error (docs-to-update.txt unreadable)` even though every sub-step is skipped.
In every mode, a `docs: error (docs-to-update.txt persistence failed)` is still reported when `docs-to-update.txt` is absent; copy it into the run's docs status file as soon as it is detected, and re-read both files (never conversation memory) when writing the final docs status.
When rewriting the docs status file, carry forward only the provisional persistence-failed line; every other `docs: error` reason (`policy unresolved`, `docs-to-update.txt unreadable`, `changed-file discovery failed`) is re-derived on each entry from that entry's own resolver run and file reads, so a fixed config or file clears it.
In `minimal` mode, never look for new documentation opportunities; a doc this change made inaccurate that the implementer did not report is first recorded as a `docs-to-update.txt` entry, then repaired.
In `minimal` mode, every `warn` and `skip` maintenance result is report-only with no prompt; a `fail` still enters the fix / stop / push-anyway choice, and `maintenance.checkDuringImplement=false` still makes a `fail` report-only.
Record each documentation validation the run applies — the checker and each repository docs check the plan names — as exactly one of `pass`, `fail`, `skipped (<reason>)` or `unavailable`; with no `flow/skills/maintain/scripts/check.sh` in the tree, record `unavailable` and render it unchecked, never as "not applicable"; a changed-file discovery failure records `validation: check.sh --changed — skipped (changed-file discovery failed)` and never enters the fail question, a checker crash records `skipped (checker execution failed)` the same way, a run with no doc-affecting changed path records `validation: check.sh --changed — skipped (no doc-affecting changes)`; only `skipped (no doc-affecting changes)` renders checked, and a halted repository check stops before commit like a halted checker.
A repository-declared docs command runs through the client's normal permission flow with no new allow-list entry.
A failing repository docs command appears in the PR body with only the command label, its exit code and a short excerpt with secret, environment-variable, token and URL-credential values removed, never its output verbatim; showing the verbatim output in the local user-input prompt stays fine.
Record that sanitized excerpt once, as one `excerpt: exit <code> — <sanitized excerpt>` line (a few output lines at most, sanitized before it is written) immediately under the failing `validation:` line in the maintenance status file; the PR body's notes and a halted stop cite that line, never reconstructed output.
Compute changed files for the checker recompute and the final docs status as the union of `git -C <abs-worktree-path> diff --name-only origin/main` and `git -C <abs-worktree-path> ls-files --others --exclude-standard` (untracked files are staged into the PR later), each captured to its own exit-checked temporary file in the run directory (or a `mktemp` file under `${TMPDIR:-/tmp}/cenci/`); a failure of either records `docs: error (changed-file discovery failed)`, never `none`.
In `minimal` mode, the PR body carries exactly one documentation status line in place of `Documentation updated`: `docs: none (minimal policy)` when the PR changes no documentation, `docs: updated <comma-separated repo-relative paths>` (every changed `README.md`, `AGENTS.md`/`CLAUDE.md` and `docs/**` path, plus plan-named doc files) otherwise, or an unchecked `docs: error (<reason>)`; an unreadable `docs-to-update.txt` reports `docs: error (docs-to-update.txt unreadable)`, never `docs: none (minimal policy)`. Maintenance failures and coverage gaps still render in the checklist and notes.
Documentation routing: right after resolving the policy, read the plan's `### Documentation Routing` section. When the plan has none (a legacy plan or the trivial plan's one-liner) or its `Discovery:` line reads `unavailable (<reason>)`, run `"${PLUGIN_ROOT}/skills/implement/scripts/doc-conventions.sh" discover "<abs-worktree-path>"` instead, reading stdout only. Any non-zero exit, or empty stdout, means discovery is unavailable, never a hard stop.
When discovery is unavailable, do not apply the `convention: none` fallbacks; read the root and project `AGENTS.md` and any docs index directly, write only to owners the plan names or those files route to, create no new file, append nothing under `## Critical Rules`, and say so in the status update.
Write each documentation edit to the discovered `owner:` path that owns its subject, never to a file picked by assumption.
When a documentation convention is discovered, never create a `docs/<topic>.md`, append to `## Critical Rules`, or write a second copy of a contract; route the write to the discovered owner.
Edit a router (`AGENTS.md`/`CLAUDE.md`) only when a new focused owner doc is added or an existing reading trigger becomes inaccurate.
Route feature contracts, component catalogs, setup detail and operational procedures to their focused owner doc instead of `AGENTS.md`.
Keep any `AGENTS.md` edit the user or the plan explicitly requires.
A `CLAUDE.md` whose only content is `@AGENTS.md` stays exactly that import; never expand or duplicate it.
Only when the routing reports `convention: none` do the fallbacks apply, and in `default` mode only: the lessons collector may create a new topic doc when findings cluster, and a project-wide invariant is appended under `## Critical Rules`; its `recommend:` block is reported as a recommendation and never applied as a migration.
Lesson capture: forward the plan's `### Documentation Routing` section (its `Lessons:` line included, or the `discover` output when the docs step ran it) and `Documentation policy: <minimal|default>` to the lessons collector, with an unresolved policy forwarded as `default`; the collector has no shell and never resolves the policy itself, and it never writes to a lesson system's admission doc or an existing record.
When discovery is available, `Owners:` names the plan's doc edits, not an exhaustive destination allowlist: the collector may find and use a fitting existing owner under root or per-project `docs/` even when `Owners: none`.
When discovery is unavailable, its restriction to plan-named or router-named owners takes precedence over independent owner search.
Rework or a reviewer flag is a lesson candidate, not an automatic admission.
An admitted lesson produces exactly one entry: one record, one flat bullet or one dropped line, never several for the same subject.
Deduplicate each candidate against existing owner docs and lesson records before any write; a duplicate produces no write and a `Dropped (already owned)` summary line.
With `Lessons: structured admission=<path> records=<dir>`, an admitted lesson is one new record in `<dir>` following the admission doc's format, never a flat bullet.
With `Lessons: fallback`, an admitted lesson is one flat bullet in the existing owner doc that owns its subject.
In `minimal` mode with no lesson system and no fitting existing owner, an admitted lesson is dropped and listed as one line in the PR's `## Notes`; no new `docs/<topic>.md` and no `## Critical Rules` bullet is written.
Only `### Dropped (minimal policy)` entries reach the lessons status file and the PR's `## Notes`; every other dropped entry stays in the collector's summary.
Write the run's lessons status file with the `Write` tool, overwriting any earlier copy with this entry's result when the collector ran in this entry: one `Lesson dropped (minimal policy): <one-line summary>` line per lesson the collector listed under `### Dropped (minimal policy)`, so the PR step can render it after a context reset.
The lessons status file write, like the maintenance check and the docs status, runs in every mode and is never skipped by a sub-step trigger or the `minimal` policy.
The lessons status file is written on every entry into this lesson-capture step, including when the collector was not launched and when no lesson trigger fired: an empty file means no lesson was dropped, a file that is absent while the run directory is known means the status is unknown, and a file that exists but cannot be read means the status is unknown with a reason, never none.
When the collector is not relaunched in this entry, read the existing lessons status file first and carry its lines forward; write an empty file only when no earlier file exists or when the collector ran in this entry and listed none, and report an earlier file that cannot be read in the status update instead of replacing it with an empty file.
When the run directory is unknown, skip the lessons status file write and name each dropped lesson in the status update; Phase 9 then renders its own `RUN_DIR lost` entry.
After writing the lessons status file, read it back and confirm its content equals what was just written, an empty file reading back as empty and never as absent or unreadable; retry the write once, and when it still does not match report that in the status update instead of leaving the PR step to read a stale or absent file as none.
An empty lessons status file counts as empty (none), not unreadable: when the client's read tool returns no content or only a system reminder for an empty file, that counts as empty, and the client's shell (`test -e` / `test -s`, or `cat` and its exit status) confirms it, rather than a reminder-only read being treated as unreadable.
When the collector returned `Blocked: …`, errored, or returned neither `## Lessons Collected` nor `No lessons captured`, write `Lesson capture unavailable (<reason>)` as the only line of the lessons status file (or name it in the status update when the run directory is unknown), with a reason of repo-relative paths and identifiers only (#979); never write an empty file for it.
Each dropped-lesson summary keeps to repo-relative paths and identifiers only, never file content, secrets, environment values, URL credentials or command output, because Phase 9 copies it into a PR body that can reach a public repository; this takes precedence over copying the collector's line verbatim (#979).
Render each `Lesson dropped (minimal policy):` line as one entry under `## Notes` → `### Considered and discarded`, using repo-relative paths and identifiers only, never file content or command output.
A dropped-lesson line that carries anything beyond a repo-relative path or an identifier (file content, a command's output, a secret, an environment value, a URL credential) is cut down to its identifier-level part before it is rendered: this secrecy rule takes precedence over copying the line verbatim (#979), because a PR body can reach a possibly-public repository.
Render a `Lesson capture unavailable (<reason>)` line as one unchecked `- [ ]` entry under `## Notes` → `### Considered and discarded` in every policy, never as "none" and never as a dropped lesson, with the reason cut down to repo-relative paths and identifiers only (#979).
When the run directory is unknown, or the lessons status file is absent or cannot be read, render one unchecked `- [ ] Lesson status unavailable (<reason>)` entry under `## Notes` → `### Considered and discarded` in every policy, never as "none", with the reason limited to `RUN_DIR lost`, `file absent` or `file unreadable` and never a path or a raw tool error (#979); the lesson-capture step always writes the file, so an absent file means the status is unknown, not that no lesson was dropped.
Any non-empty lessons status file that contains a line matching neither `Lesson dropped (minimal policy):` nor `Lesson capture unavailable (` is treated as unreadable: render `- [ ] Lesson status unavailable (file unreadable)` and copy none of the unrecognised text into the PR body (#979).
Execute only the plan's `Validations:` text verbatim; compare the collector's `### Lesson validation` report to it per command (the backticked command text, not the whole line) by exact string equality, so a command that ran is never also reported as skipped, and use the report only to derive `validation: <label> — skipped (not named in the plan)` lines, never to execute anything.
The `<label>` of a `validation: <label> — skipped (not named in the plan)` line is limited to the command name or the admission doc's repo-relative path, with environment-variable assignments, tokens and URL credentials removed; this takes precedence over verbatim copying (#979).
A lesson validation that fails after a record was created is recorded as a `fail` `validation:` line in the maintenance status file and enters the Fix now / Stop / Push anyway question, never as a pass.
Write each `validation: <label> — skipped (not named in the plan)` line for a collector-reported command into the maintenance status file with the other `validation:` lines, never as `pass`; Phase 9 renders it as an unchecked `Docs check skipped` line.
Re-derive that skipped line on every entry from the collector's `### Lesson validation` report; when the collector was not relaunched or reported none, read the earlier status file before the first-line rewrite and carry its `skipped (not named in the plan)` lines forward.
For each user-visible term the change altered or removed, run `"${PLUGIN_ROOT}/skills/implement/scripts/doc-conventions.sh" mentions "<abs-worktree-path>" '<term>'` (read-only; prints matching Markdown paths only). The term list comes from the routing's `Owners:` line and every `Docs to update:` item the implementer reported.
Pass each `<term>` single-quoted as `'<term>'`, writing any embedded `'` as `'\''`; a term is untrusted data, so never use double quotes and never let the shell expand `$`, backticks or `"` in a term.
For each user-visible term this change altered, update every file `mentions` prints that the change made inaccurate; leave `AGENTS.md` (outside the router rules) and any `README.md` that `mentions` does not print unchanged.
A doc file named in the plan's `### Files to Modify` / `### Files to Create` is written with its plan-required content in every policy, including `minimal`.
Name the owner from `### Documentation Routing` on each `Docs to update:` line, never `AGENTS.md` for a contract, catalog, setup detail or procedure.
Explicit user instructions and approved plan deliverables override every minimal-mode restriction, and no minimal-mode rule caps the length of a correct documentation update.
A nested `docs/**/*.md` path that `discover` printed as an `owner:` line or `mentions` printed is an accepted topic-doc target; the repo-relative, no-`..`, not-absolute shape guard still applies.
In default mode, including an unresolved policy, a docs status second line starting with `docs: error` adds `Documentation status: <the entire second line>` to the PR body's notes, so every `; `-joined reason survives; an unresolved policy also adds its explicit policy-unresolved note.
When the docs status file is lost, absent, unreadable or malformed (first line not exactly one of `policy: minimal`, `policy: default` or `policy: unresolved`, or `policy: minimal` with no second line starting `docs: `), re-run the resolver and read stdout only: exactly `default` keeps `Documentation updated`; `minimal` or any unresolved result renders an unchecked `docs: unknown (<why>)` line, `docs: unknown (status file malformed …)` for a malformed file, and a docs status line is never written from memory.

Before rewriting commits, in ticket mode run `cenci pipeline finalize <id>`, then resolve
the feature branch with `git -C <abs-worktree-path> symbolic-ref --quiet --short HEAD`
and read its live remote head using `git -C <abs-worktree-path> ls-remote --exit-code
origin refs/heads/<branch>`. Exit 0 must return exactly that ref and a valid commit
SHA: retain it as `<expected-head-sha>` through rebase/verification/push. Exit 2
means no remote branch exists (plain push only); other failures stop. On a fresh
attempt require the remote head to belong to local history; a divergent head needs
inspection. An interrupted publish can reuse its recorded lease only while the
live remote still equals it; never refresh that lease to overwrite another writer.
Stage this task's intended new files with ordinary `git add <paths>` so
autostash includes their content (intent-to-add is insufficient), then before
committing or pushing, run:

```bash
sh "${PLUGIN_ROOT}/hooks/scripts/rebase-remote-base.sh" "<abs-worktree-path>" main
```

This fetches `origin/main` with an explicit destination and rebases onto it,
never onto the potentially stale local `main`. It preserves uncommitted edits
with autostash and fails if either the rebase or autostash restoration conflicts.
Only exit 0 with `REBASE_STATUS=ready` allows continuing. On conflicts, resolve
in the feature worktree, preserving both branches' intent and any recovery stash;
delegate to an Opus-capable worker when available, otherwise perform the local
resolution in this session. Stop only for an unresolved product decision or an
actual fetch/config/verification failure. After resolution restart this helper.
After every successful rebase, repeat full build, tests, lint when configured,
and the maintenance check on the resulting tree before commit/push/PR. Every
re-entry restarts at this fetch/rebase step. Push normally; if a previous push
exists and rebase rewrote commits, use
`--force-with-lease=refs/heads/<branch>:<expected-head-sha>` pinned to the
remote head observed before rebasing, and stop if the lease refuses.
Then commit, push, measure the final diff with
`"${PLUGIN_ROOT}/skills/implement/scripts/pr-size.sh" "<abs-worktree-path>" origin/main --plan
"<repo-root>/.plans/<filename>"`, and open the PR with a `## Size` section carrying its
`Size: <letter> (<files> files, <lines> lines) vs ticket <letter>` line and an `## Acceptance`
section carrying the acceptance table and verdict (for a UI ticket, add the gist evidence
lines after the upload and re-run the verdict with `--ui`). A PR one size above its ticket is
flagged in the body, in the checklist, and to the founder in the final summary, and must not be
merged on green checks alone. Clear the goal
before any question/error and after PR creation. If `gh pr create` fails because a pull request for the branch already exists, run `gh pr view <branch> --json url,number,state` and treat it as a successful create only when it succeeds with a non-empty url and number and state `OPEN`; otherwise stop and report the original `gh pr create` error.
After a successful PR creation (never on a failed `gh pr create`; a recovered "already exists" counts as a success), record it in ticket mode with `cenci pipeline artifact <id> --pr <pr-url> --pr-number <n>` (if that call fails after the PR was created, the stop message prints the PR URL), then archive the consumed plan file instead of deleting it. `.plans/` lives
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
already armed it), same as an exit-0 arm — whether it armed a supervisor right here (host-armed)
or forwarded the request to the host daemon from inside `cenci sandbox` and armed one there
instead (forwarded-armed) — report which of the two happened. An arm failure whose message starts with `not armed: `
or `arm status unknown: ` never fails the run — the open PR is the real deliverable — but never
report the PR as being watched either: report "PR open, not being watched," the CLI's reason
verbatim (never re-derived or re-worded), and the host re-arm command, `cenci babysit <pr>
--agent codex`, run from a host tmux pane. Any other launch failure (auth, network, missing
binary) is reported the same not-watched way, without failing the run. Never force-push or bypass
security/design/approval gates.
