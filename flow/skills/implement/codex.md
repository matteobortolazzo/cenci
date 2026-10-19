# Codex implement procedure

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
Then return an approved plan in the conversation without writing files or labels.
Stop before mutations and instruct `cenci run implement apply <ticket> --agent codex`.
This procedure has no autonomous approval path: `/plan` always stops before
mutations and returns the plan in the conversation for a human to approve, so
every plan it persists is `approval: human` by construction and needs no
value branch of its own (`skills/implement/phases/phase-1-plan.md`'s
`## Persist the Plan` documents the key and the four autonomous values the
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
Read `### Parallelism Assessment`; a missing legacy assessment means sequential execution
with that reason reported, never inferred fan-out. Use native `implementer` subagents,
passing the worktree, plan sections, owned files, scoped test command, acceptance criteria
and context-budget brief to each. Workers are not alone in the worktree and must preserve
other workers' changes; the root alone owns shared git state and external writes.

With exactly one delegated lane, `cenci.compactImplementation` exactly true (default false)
permits a small S, low-risk, concrete plan to combine red/green/refactor in one worker.
Follow Phase 3's `## Compact Implementation`, including its exclusions, observed red-before-green
evidence, bounded Reuse Check and full build/test/lint results. Otherwise use the ordinary
separate test, implementation and refactor delegations. Compact mode never merges lanes.

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
After all waves finish, delegate one worker for Phase 4's `## Lane Verification Barrier`:
full build, test and configured lint, integration fixes, exact results, the same three-attempt
limit and context/diagnosis stops. Then use one refactor pass with the reuse check below.
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
Any consolidation made here is covered by the same full-suite-and-lint run as the rest of the
phase — re-run both before proceeding — and a reported near-duplicate is recorded for review
visibility only, never tracked or turned into a Followup ticket.
Then, before any code review, run the acceptance review: delegate the `acceptance-reviewer`
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
entry in the PR's `## Notes` (what was claimed, why it does not hold); lower-tier findings keep
their fix-now-or-discard handling. Fix the accepted findings,
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
In default mode, including an unresolved policy, a docs status second line starting with `docs: error` adds `Documentation status: <the entire second line>` to the PR body's notes, so every `; `-joined reason survives; an unresolved policy also adds its explicit policy-unresolved note.
When the docs status file is lost, absent, unreadable or malformed (first line not exactly one of `policy: minimal`, `policy: default` or `policy: unresolved`, or `policy: minimal` with no second line starting `docs: `), re-run the resolver and read stdout only: exactly `default` keeps `Documentation updated`; `minimal` or any unresolved result renders an unchecked `docs: unknown (<why>)` line, `docs: unknown (status file malformed …)` for a malformed file, and a docs status line is never written from memory.

Before rewriting commits, resolve
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
already armed it), same as an exit-0 arm — whether it armed a supervisor right here (host-armed)
or forwarded the request to the host daemon from inside `cenci sandbox` and armed one there
instead (forwarded-armed) — report which of the two happened. An arm failure whose message starts with `not armed: `
or `arm status unknown: ` never fails the run — the open PR is the real deliverable — but never
report the PR as being watched either: report "PR open, not being watched," the CLI's reason
verbatim (never re-derived or re-worded), and the host re-arm command, `cenci babysit <pr>
--agent codex`, run from a host tmux pane. Any other launch failure (auth, network, missing
binary) is reported the same not-watched way, without failing the run. Never force-push or bypass
security/design/approval gates.
