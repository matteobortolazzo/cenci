# Phase 8: Capture Lessons And Update Docs

Read this file only when Phase 8 starts.

Ticket mode: at Phase 8 start, invoke `cenci pipeline finalize <id>` to advance to `finalized` and obtain this stage's `next_actions`/`warnings`/`errors`. Render them as this phase's status update. This call runs inside the feature worktree, a different tree than where `prepare`/`plan`/`execute` ran (main checkout), but pipeline state is now anchored to the main-checkout root and reliably shared across that boundary, so a non-empty `errors[]` here is an **authoritative hard-stop**, exactly like every other stage transition in this pipeline: stop and report the errors. Do **not** fall through to the rest of this phase's prose-driven procedure when `errors[]` is non-empty. Ticketless mode: skip this call — the pipeline commands operate on ticket IDs.

Default action: skip. Only run a sub-step when its trigger fired.

All file edits must land inside `<worktree-path>`. Use absolute paths rooted at the worktree when reading/writing or delegating.

Before any sub-step writes or delegates, verify `<worktree-path>` (from Phase 2) is an **absolute** path containing a `/.worktrees/` segment. If it is relative or has no `/.worktrees/` segment, do not delegate — stop and report, since any edit would be stranded in the main worktree.

**Documentation ownership.** Phase 8 is the owner of `README.md`, `AGENTS.md`/`CLAUDE.md`, and `docs/**` updates for an implement run — earlier phases must not edit these except under the plan-named-file carve-out: a doc file explicitly named in the plan's `### Files to Modify` / `### Files to Create` stays the implementer's job and is not routed through the sub-steps below.

## Capture Lessons

Run `lessons-collector` only if at least one occurred:

- A build/test failure needed a non-obvious fix.
- The wrong API/pattern was used, then corrected.
- An assumption caused rework.
- A reviewer flagged something the implementer should have caught.

Do not run for smooth sessions, normal TDD red/green progression, or obvious findings already covered by existing rules.

Pass that exact verified `<worktree-path>` as `<project-root>`. The collector routes to existing `docs/<topic>.md`, `CLAUDE.md` Critical Rules for project-wide invariants, or a new topic doc only when multiple findings cluster. It never writes to `.claude/rules/lessons-learned.md`.

Lessons must be specific, actionable, non-duplicate, and worth keeping permanently.

## Update CLAUDE.md

**Trigger:** an entry in `$RUN_DIR/docs-to-update.txt` naming `AGENTS.md` (root or per-project) or the legacy `CLAUDE.md`, or a genuine new architectural pattern, integration rule, or project-wide convention discovered in this phase. Skip when `$RUN_DIR/docs-to-update.txt` is absent and no such pattern was discovered — treat an absent file as "none". If the file exists but a read attempt fails (permission error, I/O error), that is a genuine failure, not "none": a `Docs to update:` need was already identified and persisted in Phase 6 + 7, so silently skipping would lose it without a trace. Report `docs: error (docs-to-update.txt unreadable)` in this phase's status update instead of skipping — mirroring `## Maintenance Check`'s own report-directly-for-Phase-9 fallback below.

An `AGENTS.md` entry is written to `AGENTS.md` when that file exists, `CLAUDE.md` only as the legacy fallback.

Update only for new architectural patterns, integration rules, or project-wide conventions future work must follow.

Append under `## Critical Rules`; do not rewrite existing content.

## Update README.md

**Trigger:** an entry in `$RUN_DIR/docs-to-update.txt` naming `README.md` (root or per-project), or a genuine user-visible feature, command, API endpoint, configuration, setup, or prerequisite change discovered in this phase. Skip when `$RUN_DIR/docs-to-update.txt` is absent and no such change was discovered — treat an absent file as "none". If the file exists but a read attempt fails, treat that as a genuine failure exactly as `## Update CLAUDE.md` above does: report `docs: error (docs-to-update.txt unreadable)` in this phase's status update rather than silently skipping.

Update only for user-visible features, commands, API endpoints, configuration, setup, or prerequisites. Keep changes minimal and match existing style.

## Update Topic Docs

**Trigger:** an entry in `$RUN_DIR/docs-to-update.txt` naming a file under `docs/**`. Skip when `$RUN_DIR/docs-to-update.txt` is absent — treat that as "none". If the file exists but a read attempt fails, treat that as a genuine failure exactly as `## Update CLAUDE.md` above does: report `docs: error (docs-to-update.txt unreadable)` in this phase's status update rather than silently skipping.

For each named path, accept it only if it is repo-relative, contains no `..` path segment, is not absolute, and matches `docs/*.md` or `<project-path>/docs/*.md` for a project declared in the resolved config — never write to a path outside that shape. Skip and report any entry that doesn't match (name it in this phase's status update) rather than writing it; this sub-step's write target is a free-text path the implementer's report names, rather than being anchored to one well-known basename the way `## Update CLAUDE.md`/`## Update README.md`'s triggers are, so it needs this explicit shape guard where those two don't; without it, a malformed entry (e.g. `docs/../../.github/workflows/ci.yml`) could otherwise steer a write outside `docs/**`.

For each named `docs/<topic>.md` path, apply the implementer's reported doc-content update. This is distinct from `## Capture Lessons` above: that sub-step remains the opt-in `lessons-collector` route for genuine mistakes, while this sub-step applies the doc-content updates the implementer already identified while implementing the plan. Both may write `docs/**` in the same run — `## Capture Lessons` runs first, keeping the existing file order.

## Maintenance Check

Core correctness checks always run in Phase 8. The applicability and changed-file
guards below still produce explicit skips when the checker is not relevant, but no
maintenance configuration value disables an applicable correctness run.
`maintenance.enabled` does not gate this sub-step; it controls only optional scheduled
maintenance and reminder UX.

Resolve the optional `maintenance` object from the canonical project config with these
defaults: `checkDuringImplement=true` and `generatedDocs=true`. Test each boolean for
explicit equality with `false` — do not use a jq `//` fallback, because jq treats a real
`false` as a fallback candidate.

- When `maintenance.checkDuringImplement` is explicitly `false`, still run and report
  the checker exactly as below, but all non-pass results are report-only, including any `fail`.
  Do not enter either the auto-repair or must-stop-and-ask branch, do not mutate a finding's
  target, do not apply the CI-parity rule below, and record the results for Phase 9 with the
  `reported` outcome. This is the deliberate opt-out from the hard gate: the run may then open
  a PR that CI will fail, which is the setting's whole point.
- When `maintenance.generatedDocs` is explicitly `false`, generated-section maintenance
  is disabled: honor the checker's generated-section skip, do not run `check.sh --write`,
  and continue handling every non-generated correctness result normally.

**Applicability guard**: run this sub-step only if `<worktree-path>/flow/skills/maintain/scripts/check.sh` exists. This scopes the step to the cenci monorepo itself (dogfooding) — in a consumer repo the flow plugin is installed, not present in the target tree, so the file is absent and the step is a clean skip.

**Trigger guard**: obtain the changed-file list from `$RUN_DIR/files.txt` (written in Phase 6 + 7's Shared Context step), checking that the file read itself succeeds. Recompute the changed-file list the same way as the `$RUN_DIR`-unknown case below when any doc sub-step in this phase wrote a file (`## Capture Lessons`, `## Update CLAUDE.md`, `## Update README.md`, or `## Update Topic Docs`): do not trust the stale `$RUN_DIR/files.txt` snapshot captured back in Phase 6 + 7, before this phase's writes existed, so this phase's own doc writes are visible to the checker. If `$RUN_DIR` is unknown (lost to compaction, or a re-run in a fresh session), recompute it with `git -C <worktree-path> diff --name-only origin/main`, capturing output in a temporary file and checking the `git diff` exit status before reading it. Never use process substitution for changed-file discovery: an acquisition failure must not collapse into an empty list. If either the expected `files.txt` read or the fallback `git diff` fails, write `maintenance: error (changed-file discovery failed)` to `$RUN_DIR/maintain-status.txt` when `$RUN_DIR` is known, otherwise report that status directly for Phase 9, then stop this sub-step. Run the checker only if at least one successfully discovered changed path matches:

```
flow/skills/**
flow/agents/**
flow/hooks/**
flow/docs/**
docs/**
README.md
flow/README.md
.cenci/config.json
install.sh
sandbox/**
watch/**
.claude-plugin/marketplace.json
**/plugin/**/*.json
**/.claude-plugin/*.json
flow/opencode/install-skills.sh
sandbox/lib/migrate-settings.sh
sandbox/lib/codex-config.sh
```

If no changed path matches, write `maintenance: skipped (no doc-affecting changes)` to `$RUN_DIR/maintain-status.txt` (skip the write if `$RUN_DIR` is unknown) and stop this sub-step.

**Run**: set the shell tool working directory to the verified absolute `<worktree-path>` on every checker invocation in this sub-step, including the initial
changed-file check, `--write`, and every verification re-run. Never rely on a prior
`cd`, a relative worktree path, or a compound `cd &&` command. Invoke
`bash flow/skills/maintain/scripts/check.sh --changed <changed files>` and capture the
text output (stdout+stderr) and exit code.

**Checker-crash guard**: `check.sh` exits 2 (not 0/1) on its own infra errors (missing `jq`, `mktemp` failure, etc.) and in that path never reaches its `summary: pass=... warn=... fail=...` line. Before running the repair-policy decision flow below, check the captured output for that `summary:` line — regardless of exit code, but especially on exit 2. If it is absent, do **not** proceed into the decision flow (there are no parsed results to interpret) and do not fabricate `pass=0 warn=0 fail=0`. Instead write `maintenance: error (checker execution failed) — <captured output/stderr>` as the status file's first line (see Status file below) and stop this sub-step; Phase 9 renders this as an honest "status unknown/errored" rather than a false pass.

**CI-parity rule — a checker `fail` is a hard gate, a `warn` is not**: CI's `flow-maintenance` job runs this exact command (`check.sh --changed <changed files>`) against the same tree and exits non-zero on any `fail`. `--changed` mode already downgrades breaches unrelated to this PR's own changed paths to `warn`, which means every `fail` that survives here is one CI will raise too, so pushing it is a guaranteed red pipeline. Never treat a `fail` as advice to carry forward: it is either cleared in this run or explicitly overridden by the user. `warn` and `skip` are not CI-blocking and stay advisory. This rule is what the `fail`-status routing below implements — do not re-derive it per finding, and do not exempt a finding because its check name looks cosmetic (the checker, not this phase, decides what is a `fail`).

**Repair-policy decision flow** — for each non-pass result the checker reports:

- **Allowed (auto-repair, same PR only)**: a new-skill table row, a broken path caused by this PR's own rename, a stale generated index (`check.sh --write`, only when `maintenance.generatedDocs` is not explicitly `false`), a missing structural-test entry, a documented flag changed by this PR. Apply the fix inside `<worktree-path>`, then re-run the checker to confirm the finding cleared. If the re-run does **not** clear the finding, do not keep retrying or silently treat it as fixed, and do not blanket-file it as advice — re-classify it by its status: a surviving `fail` routes to the must-stop-and-ask branch below, a surviving `warn` to the report-only default. Either way it no longer counts toward "repaired", and the status file's `<repaired|reported|halted|overridden>` tag must reflect where it actually landed. An "Allowed" finding must never silently roll into a "repaired"/"pass" status-file line when it didn't actually clear.
- **Must stop and ask (do not auto-repair)**: **any result whose status is `fail`** that the Allowed branch did not clear (per the CI-parity rule above), plus these ambiguity cases at any status: two docs express conflicting intended behavior, client-support status is ambiguous, a rule's meaning would change, a security/workflow policy would be weakened, or the required change expands product scope. Route the question through `AskUserQuestion`. Phase 8 is not pre-approved, so halting here is safe (this is why the maintenance check lives in Phase 8, not Phase 9).
- **Report-only default**: any `warn` or `skip` finding that is neither clearly Allowed nor clearly a must-stop case is **not** silently repaired — record it in the status file and carry it forward to Phase 9's `## Notes`; do not halt. A `fail` never reaches this branch.

**The `fail` question**: ask one question per distinct failing check. State the checker's exact `FAIL <check> <target>: <message>` line and its `-> fix:` hint verbatim so the choice is made against the real finding, not a paraphrase, and offer exactly these options in this order: **Fix now**, **Stop**, **Push anyway**.

- **Fix now** — apply the checker's suggested fix inside `<worktree-path>`, then re-run the checker and re-enter this decision flow with the fresh results. A finding that clears this way counts toward `repaired`. If the re-run still reports the same `fail`, ask again rather than looping silently.
- **Stop** — end the run here. Keep the worktree, the branch, and the plan file so the fix can be made and Phase 8 re-entered; do not proceed to Phase 9.
- **Push anyway** — the user accepts a known-red pipeline. Proceed to Phase 9, which renders the failure honestly in the PR body rather than as a pass.

Map each choice to the status file: a "Push anyway" override tags the status file `overridden`; a "Stop" choice tags it `halted` and ends the run at Phase 8. Record the checker's failing lines under the tag in both cases, so Phase 9 (or a later re-entry) reports the real finding rather than only its outcome.

**Status file**: write to `$RUN_DIR/maintain-status.txt` a first-line summary, followed by the checker's non-pass lines (if any):

- If the checker reported zero non-pass results (nothing to repair, report, or halt on — the common all-clean case): `maintenance: pass=<n> warn=0 fail=0` with **no** trailing `— <tag>` suffix. This is the only case with no dash-tag; Phase 9 must not require one to recognize a clean pass.
- Otherwise, append the outcome tag: `maintenance: pass=<n> warn=<n> fail=<n> — <repaired|reported|halted|overridden>`. The counts are those of the **final** checker run in this phase (after any auto-repair re-run), not the first, so they describe the tree Phase 9 is about to push.
- If the checker's captured output had no `summary:` line (the checker-crash guard above), the first line is instead `maintenance: error (checker execution failed) — <captured output/stderr>`, with no pass/fail counts.

If `$RUN_DIR` is unknown, skip the file write; Phase 9 handles absence, matching the `review-path.txt` fallback.

**Restart-safety**: the checker and any `--write` regeneration are idempotent (re-running yields the same result on an unchanged tree), so a Phase 8 re-entry re-runs this sub-step cleanly.
