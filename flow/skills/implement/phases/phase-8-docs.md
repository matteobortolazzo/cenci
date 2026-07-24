# Phase 8: Capture Lessons And Update Docs

Read this file only when Phase 8 starts.

Ticket mode: at Phase 8 start, invoke `cenci pipeline finalize <id>` to advance to `finalized` and obtain this stage's `next_actions`/`warnings`/`errors`. Render them as this phase's status update. This call runs inside the feature worktree, a different tree than where `prepare`/`plan`/`execute` ran (main checkout), but pipeline state is now anchored to the main-checkout root and reliably shared across that boundary, so a non-empty `errors[]` here is an **authoritative hard-stop**, exactly like every other stage transition in this pipeline: clear the Goal Autopilot first (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`'s Goal Autopilot "Clearing" subsection), then stop and report the errors. Do **not** fall through to the rest of this phase's prose-driven procedure when `errors[]` is non-empty. Ticketless mode: skip this call — the pipeline commands operate on ticket IDs.

Default action: skip. Only run a sub-step when its trigger fired.

All file edits must land inside `<worktree-path>`. Use absolute paths rooted at the worktree when reading/writing or delegating.

Before any sub-step writes or delegates, verify `<worktree-path>` (from Phase 2) is an **absolute** path containing a `/.worktrees/` segment. If it is relative or has no `/.worktrees/` segment, do not delegate — clear the Goal Autopilot (`/goal clear` via `SlashCommand`, a no-op if none is armed — see `SKILL.md`), then stop and report, since any edit would be stranded in the main worktree.

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

Update only for new architectural patterns, integration rules, or project-wide conventions future work must follow.

Append under `## Critical Rules`; do not rewrite existing content.

## Update README.md

Update only for user-visible features, commands, API endpoints, configuration, setup, or prerequisites. Keep changes minimal and match existing style.

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
  the checker exactly as below, but all non-pass results are report-only. Do not enter
  either the auto-repair or must-stop-and-ask branch, do not mutate a finding's target,
  and record the results for Phase 9 with the `reported` outcome.
- When `maintenance.generatedDocs` is explicitly `false`, generated-section maintenance
  is disabled: honor the checker's generated-section skip, do not run `check.sh --write`,
  and continue handling every non-generated correctness result normally.

**Applicability guard**: run this sub-step only if `<worktree-path>/flow/skills/maintain/scripts/check.sh` exists. This scopes the step to the cenci monorepo itself (dogfooding) — in a consumer repo the flow plugin is installed, not present in the target tree, so the file is absent and the step is a clean skip.

**Trigger guard**: obtain the changed-file list from `$RUN_DIR/files.txt` (written in Phase 6 + 7's Shared Context step), checking that the file read itself succeeds. If `$RUN_DIR` is unknown (lost to compaction, or a Goal Autopilot resume in a fresh session), recompute it with `git -C <worktree-path> diff --name-only origin/main`, capturing output in a temporary file and checking the `git diff` exit status before reading it. Never use process substitution for changed-file discovery: an acquisition failure must not collapse into an empty list. If either the expected `files.txt` read or the fallback `git diff` fails, write `maintenance: error (changed-file discovery failed)` to `$RUN_DIR/maintain-status.txt` when `$RUN_DIR` is known, otherwise report that status directly for Phase 9, then stop this sub-step. Run the checker only if at least one successfully discovered changed path matches:

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

**Repair-policy decision flow** — for each non-pass result the checker reports:

- **Allowed (auto-repair, same PR only)**: a new-skill table row, a broken path caused by this PR's own rename, a stale generated index (`check.sh --write`, only when `maintenance.generatedDocs` is not explicitly `false`), a missing structural-test entry, a documented flag changed by this PR. Apply the fix inside `<worktree-path>`, then re-run the checker to confirm the finding cleared. If the re-run does **not** clear the finding, do not keep retrying or silently treat it as fixed — downgrade it to report-only: carry it forward to Phase 9's `## Notes` like any other reported finding, and reflect that in the status file's `<repaired|reported|halted>` tag (it no longer counts toward "repaired"). An "Allowed" finding must never silently roll into a "repaired"/"pass" status-file line when it didn't actually clear.
- **Must stop and ask (do not auto-repair)**: two docs express conflicting intended behavior, client-support status is ambiguous, a rule's meaning would change, a security/workflow policy would be weakened, or the required change expands product scope. Before asking, run `/goal clear` (via `SlashCommand`, a no-op if unarmed) — mirroring this phase's worktree-absoluteness guard and Phase 9's error gates, so an armed Goal Autopilot can't loop the halt — then route the question through `AskUserQuestion`. Phase 8 is not pre-approved, so halting here is safe (this is why the maintenance check lives in Phase 8, not Phase 9).
- **Report-only default**: any finding that is neither clearly Allowed nor clearly a must-stop case is **not** silently repaired — record it in the status file and carry it forward to Phase 9's `## Notes`; do not halt.

**Status file**: write to `$RUN_DIR/maintain-status.txt` a first-line summary, followed by the checker's non-pass lines (if any):

- If the checker reported zero non-pass results (nothing to repair, report, or halt on — the common all-clean case): `maintenance: pass=<n> warn=0 fail=0` with **no** trailing `— <tag>` suffix. This is the only case with no dash-tag; Phase 9 must not require one to recognize a clean pass.
- Otherwise, append the outcome tag: `maintenance: pass=<n> warn=<n> fail=<n> — <repaired|reported|halted>`.
- If the checker's captured output had no `summary:` line (the checker-crash guard above), the first line is instead `maintenance: error (checker execution failed) — <captured output/stderr>`, with no pass/fail counts.

If `$RUN_DIR` is unknown, skip the file write; Phase 9 handles absence, matching the `review-path.txt` fallback.

**Restart-safety**: the checker and any `--write` regeneration are idempotent (re-running yields the same result on an unchanged tree), so a Phase 8 re-entry re-runs this sub-step cleanly.
