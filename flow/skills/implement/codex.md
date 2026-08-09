# Codex implement procedure

Read `project-core` and `codex-runtime`. In `/plan`, gather context with the read-heavy
agent and ask material questions. When the planner's output carries a non-empty
`### Split Recommendation` or a `### Size Estimate` of `L`, the Split Gate asks, via
the client's available user-input mechanism, whether to stop — split via
`/cenci:refine`, persisting nothing — or proceed as a single PR; only Proceed
continues planning. One run persists at most one plan file and opens at most one PR:
never persist a second plan file or open a second or stacked PR for one ticket.
Then return an approved plan in the conversation without writing files or labels.
Stop before mutations and instruct `cenci run implement apply <ticket> --agent codex`.
In normal apply mode persist `.plans/<ticket>-<slug>.md`, then verify the
assembled file contains all four required headings (`## Ticket Details`, `## Implementation
Plan`, `## Architectural Context`, `## Design Context` — the `requiredPlanSections` list in
`watch/internal/pipeline/planfile.go`): if `## Design Context` alone is missing, append `##
Design Context` and `N/A`, re-check, and report the repair — if the re-check still fails, stop
before the checkpoint/label mutations and report the append failure instead; if any of the other three is
missing, stop before the checkpoint/label mutations and report the missing heading(s). Otherwise,
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
Arm
the native goal, implement test-first, run the configured reviews, fix accepted findings,
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
