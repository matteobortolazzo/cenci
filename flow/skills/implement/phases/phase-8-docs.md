# Phase 8: Capture Lessons And Update Docs

Read this file only when Phase 8 starts.

Ticket mode: at Phase 8 start, invoke `cenci pipeline finalize <id>` to advance to `finalized` and obtain this stage's `next_actions`/`warnings`/`errors`. Render them as this phase's status update. This call runs inside the feature worktree, a different tree than where `prepare`/`plan`/`execute` ran (main checkout), but pipeline state is now anchored to the main-checkout root and reliably shared across that boundary, so a non-empty `errors[]` here is an **authoritative hard-stop**, exactly like every other stage transition in this pipeline: stop and report the errors. Do **not** fall through to the rest of this phase's prose-driven procedure when `errors[]` is non-empty. Ticketless mode: skip this call — the pipeline commands operate on ticket IDs.

Default action: skip. Only run a sub-step when its trigger fired.

All file edits must land inside `<worktree-path>`. Use absolute paths rooted at the worktree when reading/writing or delegating.

Before any sub-step writes or delegates, verify `<worktree-path>` (from Phase 2) is an **absolute** path containing a `/.worktrees/` segment. If it is relative or has no `/.worktrees/` segment, do not delegate — stop and report, since any edit would be stranded in the main worktree.

**Documentation ownership.** Phase 8 is the owner of `README.md`, `AGENTS.md`/`CLAUDE.md`, and `docs/**` updates for an implement run — earlier phases must not edit these except under the plan-named-file carve-out: a doc file explicitly named in the plan's `### Files to Modify` / `### Files to Create` stays the implementer's job and is not routed through the sub-steps below.

## Resolve Documentation Policy

Run this step first, before any sub-step below. Resolve the repository's documentation policy against the verified `<worktree-path>` — the same tree every other step in this phase is rooted in, so a PR that changes `docs.policy` sees its own new value:

```bash
bash "${CLAUDE_PLUGIN_ROOT}/skills/implement/scripts/docs-policy.sh" "<worktree-path>" 2>"$RUN_DIR/docs-policy.stderr"
```

Substitute the absolute `$RUN_DIR` path in the redirect (when `$RUN_DIR` is unknown, redirect to a `mktemp` file under `${TMPDIR:-/tmp}/cenci/` instead), so stderr lands in its own capture and never mixes with the token.

`scripts/docs-policy.sh` is read-only: it never writes either config file, prints `minimal` or `default` on stdout, and exits 0 — or exits 2 with a one-line stderr error and prints nothing.

Always read the policy token from stdout only; the warning or error, if any, comes from the separate stderr capture and is never parsed for a policy.

- Exit 0 with `minimal` → apply `### Minimal mode` below.
- Exit 0 with `default` (an absent `docs` block, an absent `docs.policy`, or any value other than the exact string `"minimal"`) → default behavior: every sub-step below keeps its existing trigger, unchanged. The script writes one stderr warning naming `docs.policy` for a present but unknown value; carry that warning into this phase's status update.
- Exit 2 (an unreadable or malformed config) → apply default behavior and report `docs: error (policy unresolved)` in this phase's status update; `## Docs Status` below records `policy: unresolved`.
- Treat anything other than exit 0 with stdout exactly `minimal` or `default` — exit 2, exit 126/127 (the script could not run), any other exit status, or empty or unexpected stdout — as unresolved, exactly like exit 2: apply default behavior, record `policy: unresolved`, and report `docs: error (policy unresolved)`.

If Phase 6 + 7's status update reported `docs: error (docs-to-update.txt persistence failed)`, copy that line into `$RUN_DIR/docs-status.txt` now with the `Write` tool, right after resolving the policy and before any sub-step — as the two lines `policy: <resolved policy>` and `docs: error (docs-to-update.txt persistence failed)` — so the error survives a compaction instead of living only in this conversation (`## Docs Status` below re-reads it). Skip this when `$RUN_DIR` is unknown.

### Minimal mode

In `minimal` mode Phase 8 does documentation work only when at least one of these four conditions holds:

- **Plan-named doc file** — the plan's `### Files to Modify` / `### Files to Create` names a documentation file (the carve-out above).
- **`$RUN_DIR/docs-to-update.txt` entry** — the file lists at least one entry.
- **Changed reading trigger** — the run added, renamed or removed a doc that an `AGENTS.md`/`CLAUDE.md` read-when pointer routes to, or made such a pointer inaccurate.
- **Admitted lesson** — one of the four `## Capture Lessons` triggers fired, and the lesson is a non-obvious failure or rework that a written lesson could prevent and that no existing doc already covers.

Evaluate the `$RUN_DIR/docs-to-update.txt` entry condition by reading `$RUN_DIR/docs-to-update.txt` itself with the `Read` tool — never from memory of Phase 6 + 7, and even when every other condition already fails:
- an absent file (the read reports that no file exists at that path) means no entry;
- a file that exists but cannot be read records `docs: error (docs-to-update.txt unreadable)` even though every sub-step is skipped — carry it into this phase's status update and `## Docs Status` below; the condition then counts as unknown, never as "no entry".
- When `$RUN_DIR` is unknown, the condition cannot be read: say so in this phase's status update; Phase 9 then renders its unchecked `docs: unknown` line.

In `minimal` mode, a run with no plan-named doc file, no `$RUN_DIR/docs-to-update.txt` entry, no changed reading trigger and no admitted lesson makes zero documentation edits, skips the `## Update CLAUDE.md`, `## Update README.md` and `## Update Topic Docs` sub-steps, and does not launch `lessons-collector`.

When a condition holds, each sub-step below runs only for the condition that names it: `## Update CLAUDE.md` for its `$RUN_DIR/docs-to-update.txt` entries and a changed reading trigger, `## Update README.md` and `## Update Topic Docs` for their `$RUN_DIR/docs-to-update.txt` entries, and `## Capture Lessons` for an admitted lesson. Phase 8 never looks for new documentation opportunities in `minimal` mode: the "discovered in this phase" half of each sub-step's trigger does not apply.

Correctness work still runs in `minimal` mode. When Phase 8 finds a doc this change made inaccurate that `$RUN_DIR/docs-to-update.txt` does not list, first record it as an entry in `$RUN_DIR/docs-to-update.txt` with the `Write` tool, then repair it through the sub-step that owns its path (when `$RUN_DIR` is unknown, repair it and name it in this phase's status update). `## Maintenance Check` and `## Docs Status` run in every mode.

## Capture Lessons

**Minimal mode:** In `minimal` mode, launch `lessons-collector` only for an admitted lesson (see `### Minimal mode` above); otherwise do not launch it.

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

**Minimal mode:** run only for a `$RUN_DIR/docs-to-update.txt` entry naming `AGENTS.md`/`CLAUDE.md` or for a changed reading trigger (see `### Minimal mode` above) — never for a pattern discovered in this phase.

An `AGENTS.md` entry is written to `AGENTS.md` when that file exists, `CLAUDE.md` only as the legacy fallback.

Update only for new architectural patterns, integration rules, or project-wide conventions future work must follow.

Append under `## Critical Rules`; do not rewrite existing content.

## Update README.md

**Trigger:** an entry in `$RUN_DIR/docs-to-update.txt` naming `README.md` (root or per-project), or a genuine user-visible feature, command, API endpoint, configuration, setup, or prerequisite change discovered in this phase. Skip when `$RUN_DIR/docs-to-update.txt` is absent and no such change was discovered — treat an absent file as "none". If the file exists but a read attempt fails, treat that as a genuine failure exactly as `## Update CLAUDE.md` above does: report `docs: error (docs-to-update.txt unreadable)` in this phase's status update rather than silently skipping.

**Minimal mode:** run only for a `$RUN_DIR/docs-to-update.txt` entry naming `README.md` (see `### Minimal mode` above) — never for a change discovered in this phase.

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

**Applicability guard**: run the checker only if `<worktree-path>/flow/skills/maintain/scripts/check.sh` exists. This scopes the checker to the cenci monorepo itself (dogfooding) — in a consumer repo the flow plugin is installed, not present in the target tree, so the file is absent.
When it is absent, write `maintenance: unavailable (no documentation checker in this repository)` as the first line of `$RUN_DIR/maintain-status.txt` (report it directly for Phase 9 when `$RUN_DIR` is unknown), followed by `validation: check.sh --changed — unavailable`, then continue with **Repository docs checks** below — an absent checker is `unavailable`, never a silent skip and never `skipped`.

**Trigger guard**: obtain the changed-file list from `$RUN_DIR/files.txt` (written in Phase 6 + 7's Shared Context step), checking that the file read itself succeeds. Recompute the changed-file list the same way as the `$RUN_DIR`-unknown case below when any doc sub-step in this phase wrote a file (`## Capture Lessons`, `## Update CLAUDE.md`, `## Update README.md`, or `## Update Topic Docs`): do not trust the stale `$RUN_DIR/files.txt` snapshot captured back in Phase 6 + 7, before this phase's writes existed, so this phase's own doc writes are visible to the checker. If `$RUN_DIR` is unknown (lost to compaction, or a re-run in a fresh session), recompute it as the union of `git -C <worktree-path> diff --name-only origin/main` and `git -C <worktree-path> ls-files --others --exclude-standard` — untracked files are part of the PR, because Phase 9's `git add -A` stages them later, so a newly created file must be visible here.
Capture each command's output in its own temporary file under `$RUN_DIR` (or, when `$RUN_DIR` is unknown, a `mktemp` file under `${TMPDIR:-/tmp}/cenci/`), checking the `git diff` exit status before reading it, and likewise the `git ls-files` and `mktemp` exit statuses before reading theirs. Never use process substitution for changed-file discovery: an acquisition failure must not collapse into an empty list. If the `files.txt` read or either fallback command fails, write `maintenance: error (changed-file discovery failed)` to `$RUN_DIR/maintain-status.txt` when `$RUN_DIR` is known, otherwise report that status directly for Phase 9, followed by `validation: check.sh --changed — skipped (changed-file discovery failed)`, then continue with **Repository docs checks** below. Run the checker only if at least one successfully discovered changed path matches:

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

If no changed path matches, write `maintenance: skipped (no doc-affecting changes)` to `$RUN_DIR/maintain-status.txt` (skip the write if `$RUN_DIR` is unknown), followed by `validation: check.sh --changed — skipped (no doc-affecting changes)`, then continue with **Repository docs checks** below.

**Run**: set the shell tool working directory to the verified absolute `<worktree-path>` on every checker invocation in this sub-step, including the initial
changed-file check, `--write`, and every verification re-run. Never rely on a prior
`cd`, a relative worktree path, or a compound `cd &&` command. Invoke
`bash flow/skills/maintain/scripts/check.sh --changed <changed files>` and capture the
text output (stdout+stderr) and exit code.

**Checker-crash guard**: `check.sh` exits 2 (not 0/1) on its own infra errors (missing `jq`, `mktemp` failure, etc.) and in that path never reaches its `summary: pass=... warn=... fail=...` line. Before running the repair-policy decision flow below, check the captured output for that `summary:` line — regardless of exit code, but especially on exit 2. If it is absent, do **not** proceed into the decision flow (there are no parsed results to interpret) and do not fabricate `pass=0 warn=0 fail=0`. Instead write `maintenance: error (checker execution failed) — <captured output/stderr>` as the status file's first line (see Status file below), followed by `validation: check.sh --changed — skipped (checker execution failed)`, then continue with **Repository docs checks** below; Phase 9 renders this as an honest "status unknown/errored" rather than a false pass.

**CI-parity rule — a checker `fail` is a hard gate, a `warn` is not**: CI's `flow-maintenance` job runs this exact command (`check.sh --changed <changed files>`) against the same tree and exits non-zero on any `fail`. `--changed` mode already downgrades breaches unrelated to this PR's own changed paths to `warn`, which means every `fail` that survives here is one CI will raise too, so pushing it is a guaranteed red pipeline. Never treat a `fail` as advice to carry forward: it is either cleared in this run or explicitly overridden by the user. `warn` and `skip` are not CI-blocking and stay advisory. This rule is what the `fail`-status routing below implements — do not re-derive it per finding, and do not exempt a finding because its check name looks cosmetic (the checker, not this phase, decides what is a `fail`).

**Repair-policy decision flow** — for each non-pass result the checker reports:

- **Allowed (auto-repair, same PR only)**: a new-skill table row, a broken path caused by this PR's own rename, a stale generated index (`check.sh --write`, only when `maintenance.generatedDocs` is not explicitly `false`), a missing structural-test entry, a documented flag changed by this PR. Apply the fix inside `<worktree-path>`, then re-run the checker to confirm the finding cleared. If the re-run does **not** clear the finding, do not keep retrying or silently treat it as fixed, and do not blanket-file it as advice — re-classify it by its status: a surviving `fail` routes to the must-stop-and-ask branch below, a surviving `warn` to the report-only default. Either way it no longer counts toward "repaired", and the status file's `<repaired|reported|halted|overridden>` tag must reflect where it actually landed. An "Allowed" finding must never silently roll into a "repaired"/"pass" status-file line when it didn't actually clear.
- **Must stop and ask (do not auto-repair)**: **any result whose status is `fail`** that the Allowed branch did not clear (per the CI-parity rule above), plus these ambiguity cases at any status: two docs express conflicting intended behavior, client-support status is ambiguous, a rule's meaning would change, a security/workflow policy would be weakened, or the required change expands product scope. Route the question through `AskUserQuestion`. Phase 8 is not pre-approved, so halting here is safe (this is why the maintenance check lives in Phase 8, not Phase 9).
- **Report-only default**: any `warn` or `skip` finding that is neither clearly Allowed nor clearly a must-stop case is **not** silently repaired — record it in the status file and carry it forward to Phase 9's `## Notes`; do not halt. A `fail` never reaches this branch.
- **Minimal mode (`docs.policy: "minimal"`)**: every `warn` and `skip` result is report-only with no `AskUserQuestion` prompt, including the must-stop-and-ask ambiguity cases at those statuses; a `fail` still enters the Fix now / Stop / Push anyway question below, and `maintenance.checkDuringImplement` explicitly `false` still makes a `fail` report-only. The Allowed auto-repairs above still apply.

**The `fail` question**: ask one question per distinct failing check. State the checker's exact `FAIL <check> <target>: <message>` line and its `-> fix:` hint verbatim so the choice is made against the real finding, not a paraphrase, and offer exactly these options in this order: **Fix now**, **Stop**, **Push anyway**.

- **Fix now** — apply the checker's suggested fix inside `<worktree-path>`, then re-run the checker and re-enter this decision flow with the fresh results. A finding that clears this way counts toward `repaired`. If the re-run still reports the same `fail`, ask again rather than looping silently.
- **Stop** — end the run here. Keep the worktree, the branch, and the plan file so the fix can be made and Phase 8 re-entered; do not proceed to Phase 9.
- **Push anyway** — the user accepts a known-red pipeline. Proceed to Phase 9, which renders the failure honestly in the PR body rather than as a pass.

Map each choice to the status file: a "Push anyway" override tags the status file `overridden`; a "Stop" choice tags it `halted` and ends the run at Phase 8. Record the checker's failing lines under the tag in both cases, so Phase 9 (or a later re-entry) reports the real finding rather than only its outcome.

**Status file**: write to `$RUN_DIR/maintain-status.txt` a first-line summary, followed by the checker's non-pass lines (if any):

- If the checker reported zero non-pass results (nothing to repair, report, or halt on — the common all-clean case): `maintenance: pass=<n> warn=0 fail=0` with **no** trailing `— <tag>` suffix. This is the only case with no dash-tag; Phase 9 must not require one to recognize a clean pass.
- Otherwise, append the outcome tag: `maintenance: pass=<n> warn=<n> fail=<n> — <repaired|reported|halted|overridden>`. The counts are those of the **final** checker run in this phase (after any auto-repair re-run), not the first, so they describe the tree Phase 9 is about to push.
- If the checker's captured output had no `summary:` line (the checker-crash guard above), the first line is instead `maintenance: error (checker execution failed) — <captured output/stderr>`, with no pass/fail counts.
- After the first line and the checker's non-pass lines, write exactly one `validation: <label> — <status>[ — <tag>]` line per documentation validation this run applies — cenci's checker (label `check.sh --changed`) and each repository docs check the plan names (label: the command exactly as the plan names it) — where `<status>` is exactly one of `pass`, `fail`, `skipped (<reason>)` or `unavailable`, and only a `fail` carries a `<tag>` (`reported`, `halted` or `overridden`, mapped exactly as the first line's tag).
- The checker's own `validation:` status follows its first line: a final run with `fail=0` (including a warn-only run, since `warn` is not CI-blocking) records `pass`; a final run with `fail` above zero records `fail` with its tag; `maintenance: skipped (no doc-affecting changes)` records `skipped (no doc-affecting changes)`; `maintenance: error (<reason>)` records `skipped (<reason>)`; an absent checker records `unavailable`.

If `$RUN_DIR` is unknown, skip the file write; Phase 9 handles absence, matching the `review-path.txt` fallback.

**Repository docs checks**: the early stops above (`unavailable`, `skipped`, `error`) end only the cenci-checker part of this sub-step — this paragraph always runs, in every mode. For each repository docs check the plan names (a documentation validation command the plan lists; ticket #1302's `### Documentation Routing` becomes its structured source), run it with the shell tool working directory set to the verified absolute `<worktree-path>`, through the client's normal permission flow — add no new permission allow-list entry for it. Exit 0 records `pass`.
A non-zero exit records `fail` and enters the Fix now / Stop / Push anyway question above — stating the command and its failing output verbatim in place of a checker line, with the same `halted`/`overridden` tag mapping — unless `maintenance.checkDuringImplement` is explicitly `false`, which records it `reported`.
For a failing command, immediately under its failing `validation:` line in `$RUN_DIR/maintain-status.txt`, write exactly one `excerpt: exit <code> — <sanitized excerpt>` line: a few lines of its output at most, joined onto that one line, with secret, environment-variable, token and URL-credential values removed (when `$RUN_DIR` is unknown, report the same line directly for Phase 9). In the PR body's `## Notes`, a failing repository docs command gets only the command label, its exit code and a short excerpt with secret, environment-variable, token and URL-credential values removed — never its output verbatim, because a PR body can reach a possibly-public repository; showing the verbatim output in the local `AskUserQuestion` prompt stays fine. This secrecy rule takes precedence over any instruction to copy failing output into the PR body, including the `excerpt:` line itself: sanitize it before writing it, never after.
A command the user denies at the permission prompt records `skipped (permission denied)`; a command that cannot start records `skipped (<reason>)`. Never record `pass` for a check that did not run. Append each result as its `validation:` line (followed by its `excerpt:` line for a `fail`) to `$RUN_DIR/maintain-status.txt` (rewrite the file with the `Write` tool, keeping every earlier line). A plan that names no repository docs check adds no line.

**Restart-safety**: the checker and any `--write` regeneration are idempotent (re-running yields the same result on an unchanged tree), so a Phase 8 re-entry re-runs this sub-step cleanly; a re-entry rewrites `$RUN_DIR/maintain-status.txt` from its first line, so no stale `validation:` line survives.

## Docs Status

Run last, on every entry into this phase and in every mode — after `## Maintenance Check`, so its repairs count. Write `$RUN_DIR/docs-status.txt` with the `Write` tool, overwriting any earlier copy (a Phase 8 re-entry replaces it), as two lines:

1. `policy: minimal`, `policy: default`, or `policy: unresolved` (the resolver in `## Resolve Documentation Policy` gave anything other than exit 0 with stdout exactly `minimal` or `default`).
2. Exactly one documentation status line, the first of these that applies:
   - `docs: error (<reason>)` when a documentation error applies — `docs: error (policy unresolved)`, `docs: error (docs-to-update.txt unreadable)`, `docs: error (changed-file discovery failed)`, or Phase 6 + 7's `docs: error (docs-to-update.txt persistence failed)`; join several reasons with `; `.
   - `docs: updated <comma-separated repo-relative paths>` listing every documentation path in the run's final diff within this phase's ownership classes — `README.md`, `AGENTS.md`/`CLAUDE.md`, and `docs/**`, root or per-project — plus every plan-named doc file.
   - `docs: none (minimal policy)` in `minimal` mode, or `docs: none` otherwise — only when the PR changes no documentation at all.

Determine the error reasons from files, not from this conversation's memory of earlier steps:
- re-read `$RUN_DIR/docs-to-update.txt` here with the `Read` tool rather than relying on this conversation — a file that exists but cannot be read is `docs: error (docs-to-update.txt unreadable)`; an absent file is no error on its own.
- re-read `$RUN_DIR/docs-status.txt` before overwriting it, and carry forward only the provisional `docs: error (docs-to-update.txt persistence failed)` line it may hold (the copy `## Resolve Documentation Policy` wrote) — no file can re-derive that reason.
- every other reason (`policy unresolved`, `docs-to-update.txt unreadable`, `changed-file discovery failed`) is re-derived on each entry from this entry's own resolver run and file reads, never carried forward from an earlier copy, so a fixed config or file clears it.
- Phase 6 + 7's `docs: error (docs-to-update.txt persistence failed)` is still emitted when `$RUN_DIR/docs-to-update.txt` is absent: a persistence failure leaves no file behind, so here an absent file is not "no entry". To keep it recoverable, the status update line carrying it must be copied into `$RUN_DIR/docs-status.txt` at the point of detection — the `## Resolve Documentation Policy` step above — never held only in memory.

Obtain the final diff as the union of `git -C <worktree-path> diff --name-only origin/main` and `git -C <worktree-path> ls-files --others --exclude-standard` — untracked files count, because Phase 9's `git add -A` stages them, so a newly created doc is part of the PR.
Capture each command's output in its own temporary file under `$RUN_DIR` (or, when `$RUN_DIR` is unknown, a `mktemp` file under `${TMPDIR:-/tmp}/cenci/`), and check each command's exit status — and the `mktemp` call's — before reading its file; a failure of either records `docs: error (changed-file discovery failed)`, never `none`.

In `minimal` mode, an unreadable `$RUN_DIR/docs-to-update.txt` still reports `docs: error (docs-to-update.txt unreadable)`, never `docs: none (minimal policy)`: a file that exists but cannot be read is a lost requirement, not an absence.

If `$RUN_DIR` is unknown, skip the write and report both lines in this phase's status update; Phase 9 re-resolves the policy and renders an unchecked `docs: unknown` line.
