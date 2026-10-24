# Phase 6 + 7: Security, Code, And Silent-Failure Review

Read this file only when Phase 6 + 7 starts.

Ticket mode: at entry, invoke `cenci pipeline review <id>` to advance to `reviewed` and obtain this stage's `next_actions`/`warnings`/`errors`. Render them as this phase's status update — the lite-docs branch below renders this same call's `next_actions` instead of narrating its own transition (see Execution). This call runs inside the feature worktree, a different tree than where `prepare`/`plan`/`execute` ran (main checkout), but pipeline state is now anchored to the main-checkout root and reliably shared across that boundary, so a non-empty `errors[]` here is an **authoritative hard-stop**, exactly like every other stage transition in this pipeline: stop and report the errors. Do **not** fall through to the rest of this phase's prose-driven procedure when `errors[]` is non-empty. Ticketless mode: skip this call — the pipeline commands operate on ticket IDs.

These quality gates are mandatory. `cenci.reviewConcurrency` controls only whether reviewers run in parallel or sequentially. The `## Acceptance Review` below runs first, on every review path, before any code reviewer is launched — a partial or missing acceptance item returns the run to implementation before code review starts.

Any point below that stops for the user — an unclear security fix, a code-review human decision, an issue that persists after 2 fix attempts — is an error gate: stop and ask.

## Shared Context

Source `ticketId`/`slug` from the plan front matter (`hasPlanFile` is always true here) before writing or reading any temp file below.

**Run Artifact Directory.** Before writing any artifact below, obtain this run's artifact directory by running `flow/skills/implement/scripts/run-artifact-dir.sh` and capturing its stdout as `RUN_DIR`. Verify the script exited zero and `RUN_DIR` is a non-empty, existing directory before proceeding — if either check fails, treat it the same as any other unrecoverable Phase 6 + 7 setup failure: stop and report. `mktemp -d` inside the script guarantees `RUN_DIR` is unique and unpredictable per invocation, so two concurrent runs for the same ticket never resolve to the same location and can never read or overwrite each other's diff, file list, stat, or review-path result.

Create `RUN_DIR` **once**, on first entry into this phase. Every fix-and-rerun cycle below (including the re-eval loop) reuses the same `RUN_DIR` and only overwrites the files inside it — never call the helper again within the same phase run, or the second call orphans the first `RUN_DIR` and any reviewer already holding the old paths silently goes stale. (If Phase 6 + 7 is entered fresh in a new session — e.g. a re-run that lost `RUN_DIR` to context compaction — a new `RUN_DIR` is created and the prior run's directory becomes an acceptable ephemeral `/tmp` leak; see Phase 9's Cleanup for the matching fail-closed note.)

Gather context once. Target the worktree explicitly with `git -C <worktree-path>` on each `git diff` call so it resolves against the worktree and stays auto-approved; redirect to an absolute temp-file path (never compound `cd <worktree-path>` with the redirect). The `git ls-files` capture below also targets the worktree with `git -C <worktree-path>`.

```bash
git -C <worktree-path> diff > "$RUN_DIR/diff.patch"
git -C <worktree-path> diff --stat > "$RUN_DIR/stat.txt"
rm -f "$RUN_DIR/files.txt" "$RUN_DIR/files-tracked.txt" "$RUN_DIR/untracked.txt"
git -C <worktree-path> diff --name-only origin/main > "$RUN_DIR/files-tracked.txt"
git -C <worktree-path> ls-files --others --exclude-standard > "$RUN_DIR/untracked.txt"
sort -u "$RUN_DIR/files-tracked.txt" "$RUN_DIR/untracked.txt" > "$RUN_DIR/files.txt"
```

`$RUN_DIR/files.txt` is the changed-file list: the tracked diff against `origin/main` plus the untracked, non-ignored files (nothing is committed before Phase 9's `git add -A`, so every file this run creates is untracked until then). Run each line above as its own command and check its exit status before reading its output.

If either capture command exits non-zero (for example, `origin/main` cannot be resolved), treat it as an unrecoverable Phase 6 + 7 setup failure, the same handling as a failed `run-artifact-dir.sh`: stop and report, do not run the combine step, and never continue with an empty or partial list. If the combine step itself exits non-zero, run `rm -f "$RUN_DIR/files.txt"` and stop and report the same way, because a redirect creates the file even when the command fails. On a non-zero exit from either capture command or from the combine step, first run `rm -f "$RUN_DIR/files.txt" "$RUN_DIR/files-tracked.txt" "$RUN_DIR/untracked.txt"` (a shell redirect creates its output file even when the command fails, so a failed step can leave an empty or partial list behind), then stop and report.

Never combine the captures with a pipe such as `{ ...; ...; } | sort`, with process substitution, or with one combined command substitution: each command is captured to its own file and exit-checked on its own, so a failure cannot collapse into an empty list. See the plugin's `docs/shell-scripting-gotchas.md`. Keep `$RUN_DIR/untracked.txt`: Review Path Classification reads it below.

**Carry Phase 5's Reuse Check report forward.** Phase 5 (or, in compact mode, Phase 3's folded delegation) reports any near-duplicate it kept in place as one `Reuse Check:` line in its summary. That summary is conversation state, and Phase 9 may assemble the PR body in a compacted or fresh session, so persist those lines here — where `RUN_DIR` first exists — rather than relying on them still being in context:

```bash
printf '%s\n' "<Reuse Check: line>" ... >> "$RUN_DIR/reuse-notes.txt"
```

Write nothing when Phase 5 reported none (the common case); Phase 9 treats an absent file as "none". Do this once, on first entry into this phase, alongside the artifacts above — a fix-and-rerun cycle must not append the same lines twice.

**Carry the implementer's `Docs to update:` report forward.** Phase 4 (or, in compact mode, Phase 3's folded delegation, or Phase 5) reports needed doc updates as `Docs to update:` lines in its summary. Persist them here, alongside `reuse-notes.txt`, so Phase 8 can consume them even in a compacted or fresh session. Use the client's file tool (`Write`) to create `$RUN_DIR/docs-to-update.txt` directly with the reported lines — never a shell `printf`/`>>` interpolating the implementer's free-text report, which can contain `"`, a backtick, or `$(...)` and break out of a double-quoted shell argument (see the `shell-rules` skill's `## Body Files and Heredocs`). Confirm the write succeeded (the file exists and its content matches what was written) before treating the persistence as done. If that confirmation fails, retry the write once; if it still fails, report `docs: error (docs-to-update.txt persistence failed)` for Phase 9 rather than silently continuing — mirroring the same error-reporting shape `## Update CLAUDE.md`'s trigger uses in `phase-8-docs.md` for its own read-failure case.

Write nothing when the report was the literal `None.` (the common case); Phase 8 treats an absent file as "none". Do this once, on first entry into this phase, never re-appended by a fix-and-rerun cycle. In Parallel Lanes, or when phases 3/4/5 are folded into one delegation, concatenate and de-duplicate each report's `Docs to update:` lines before writing them here.

For small diffs and `cenci.diffContextMode` not set to `"file"`, inline the diff. For large diffs or file mode, pass reviewers the patch path, changed file list, stat, ticket requirements, and implementation plan. Tell reviewers to read only relevant hunks/files.

Source ticket requirements and plan from the plan file when `hasPlanFile` is true.

## Acceptance Review

Runs after Shared Context and **before** Review Path Classification, on every path (`full`, `lite-docs`, `lite-small` alike — the classification below never skips it). Ticketless mode: skip, and record `acceptance: n/a (ticketless — no acceptance criteria to grade)` for Phase 9's `## Acceptance` section. Ticket mode:

1. **Delegate to the `acceptance-reviewer` agent** with, by path never by paste: the plan file path (its `## Ticket Details` is the ticket body; `## Implementation Plan`, `## Q&A from Planning` and `### Decisions`/`### Design Direction` are the design record), `$RUN_DIR/diff.patch` and `$RUN_DIR/files.txt`, the worktree path, `isUiTicket`, and — for a UI ticket — the screenshot directory `${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/`. Tell it this is the pipeline's **pre-gist** pass (Phase 9 uploads the gist later), so its UI evidence lines use the `capture` form for local screen captures. It reads the ticket before the code and never grades the diff alone; it makes no code-quality comments.
2. **Persist the table.** Use the `Write` tool to create `$RUN_DIR/acceptance.md` with the reviewer's `## Acceptance Review` output verbatim (table, `Evidence:` lines, verdict). Confirm the write landed before the next step.
3. **Compute the verdict mechanically** — the script, not the reviewer's prose, gates the pipeline:
   ```bash
   bash "${CLAUDE_PLUGIN_ROOT}/skills/review/scripts/acceptance-verdict.sh" "$RUN_DIR/acceptance.md"
   ```
   append `--ui-local` when `isUiTicket` is true (requires `Evidence: gallery en-US`, `gallery it-IT`, `capture en-US`, `capture it-IT` lines — a UI ticket reviewed from the diff alone fails here by construction). Record the first output line (`covered=… partial=… missing=… unrequested=… verdict=…`) to `$RUN_DIR/acceptance-verdict.txt`. Exit 2 (malformed table, zero rows) is treated as a fail: re-delegate the reviewer once with the script's message; a second malformed output is an error gate — stop and ask the user.
4. **Fail → back to implementation, before any code review.** On exit 1, delegate the `implementer` (Phase 3 → Phase 4 discipline: failing test first, then green, lane-scoped, with the `## Context Budget` brief) with exactly the `blocking:` rows the script printed (each partial/missing item, verbatim, and for a UI ticket each `ui-evidence-missing:` capture) — never the whole table re-read as a to-do list. After it reports green on the fix's scoped Verification command (the cell of each lane that owns a changed file; reported as `Verification (scoped): …`), do not run the full suite per round: a report that lists a changed file marks the Completed-Change Verification evidence stale (see `## Completed-Change Verification Re-run` below). Then regenerate the Shared Context artifacts into the same `$RUN_DIR`, and re-run steps 1-3. At most **two** such rounds; if the verdict is still `fail`, stop at an error gate via `AskUserQuestion` listing the blocking rows (the ticket may be mis-sized or mis-specified — `/cenci:refine <id>` is the usual answer). `unrequested` rows never block: they are carried into the PR body's `## Acceptance` table for the human and are visible to `code-reviewer` as scope context.
5. **Pass → continue** to Review Path Classification. Only a `verdict=pass` ever reaches the code reviewers.

## Review Path Classification

Classify this diff into one of three review paths before launching any reviewer. Before evaluating any rule, delete/overwrite `$RUN_DIR/review-path.txt` (e.g. `rm -f`) so a stale value from an earlier run of this ticket can never be trusted if this classification is interrupted partway (e.g. context compaction mid-phase) — an absent file is safer than a stale one: it causes Phase 9 to report the review status as honestly unknown rather than trusting a value that may no longer match what actually ran. Evaluate the rules below **in order — first match wins**. Read `$RUN_DIR/files.txt` (changed file list), `$RUN_DIR/untracked.txt` (untracked files only) and `$RUN_DIR/stat.txt` (total changed lines) from the Shared Context step above.

**Danger check** (used by Rules 2 and 3 below): a diff matches the danger check if **either** of these hold, case-insensitive:

- Any changed path contains one of: `auth`, `security`, `secret`, `credential`, `token`, `password`, `payment`, `.env`, `.github/workflows/**`.
- The diff content itself (added/removed lines in `$RUN_DIR/diff.patch`) contains one of the same keywords — this catches an innocuously-named file whose diff content adds something like `"apiSecret": "..."`. The same keyword scan also covers the full content of every file listed in `$RUN_DIR/untracked.txt`, because `diff.patch` never contains an untracked file's content; a file listed there that cannot be read counts as a match (fail closed, like Rule 4).

0. **Toggle off**: if the resolved config sets `cenci.liteReviewEnabled` to `false` → path = **full**. Skip the remaining rules entirely.
1. **Force-full override**: if any changed file matches `.claude/**`, `**/skills/**`, `**/agents/**`, or `CLAUDE.md` (root or any nested `**/CLAUDE.md`) → path = **full**, regardless of size or file type. This check runs before the docs-only check below, so a `CLAUDE.md`-only or `skills/**`-only change never qualifies as docs-only, no matter how small.
2. **Docs-only**: if every changed file is a prose file — `README.md`, `docs/**`, `LICENSE`, `CHANGELOG.md`, `CONTRIBUTING.md`, or any other `**/*.md` **outside** the force-full set above — **and** `$RUN_DIR/untracked.txt` exists (a missing file never qualifies as lite-docs, because its untracked content could not be danger-scanned; it falls through to Rule 4 and resolves to **full**) **and** the diff does not match the danger check above → path = **lite-docs**. No reviewers run at all. If every file is prose but the danger check matches (e.g. `docs/security-notes.md`, `SECURITY.md`, or a danger keyword in the diff content), do not take lite-docs — fall through to Rule 3/4 instead; a docs-only diff can still leak a credential or contain security-sensitive prose, so skipping all review here is the highest-risk gap.
3. **Small + safe**: if **all** of the following hold → path = **lite-small**:
   - Total changed lines (insertions + deletions, from `$RUN_DIR/stat.txt`) < 20.
   - Every changed file is on the config/data allowlist: `*.json`, `*.yaml`/`*.yml` (excluding `.github/workflows/**`), `*.toml`, `*.txt`, `*.csv`, or another plain data/fixture file. Source/logic file extensions (`.ts`, `.tsx`, `.js`, `.jsx`, `.go`, `.py`, `.rb`, `.cs`, `.sh`, `.java`, `.rs`, or any other language/logic extension) are **never** on this allowlist, no matter how small the diff — a one-line source fix is never lite-small.
   - `$RUN_DIR/untracked.txt` is empty (it exists and lists no path): `stat.txt` is a bare `git diff --stat` that does not count untracked lines, so a diff containing any untracked file can never be called lite-small.
   - The diff does not match the danger check above.
4. **Otherwise** (ambiguous, over threshold, or any file off the allowlist) → path = **full**. Err toward full when uncertain. Ambiguity includes: `$RUN_DIR/files.txt` or `$RUN_DIR/stat.txt` is missing, empty, or the changed-line count cannot be confidently parsed as a number — resolve any of these to **full** before assuming 0 changed lines. A missing `$RUN_DIR/untracked.txt` is also ambiguous and resolves to **full** (an existing empty one is valid: it means no untracked files).

Write the chosen path string — `full`, `lite-docs`, or `lite-small` — to `$RUN_DIR/review-path.txt`.

## Execution

Branch on the classification result written above:

- **`lite-docs`**: launch no code reviewers (the `## Acceptance Review` above already ran and passed). Docs-only changes skip the code-review trio entirely; render the `next_actions` from the `cenci pipeline review <id>` call above (see the entry note) as this run's status update instead of narrating what comes next.
- **`lite-small`**: launch `code-reviewer` only, with the same diff/path, changed files, ticket requirements, and implementation plan it would receive on the full path. The existing Code Review Actions below apply unchanged — this is a narrower reviewer set, not a cheaper model tier. `cenci.reviewConcurrency` has no effect with a single reviewer.
- **`full`**: launch all three reviewers:

  - `security-reviewer`: diff/path plus changed files.
  - `code-reviewer`: diff/path, changed files, ticket requirements, implementation plan.
  - `silent-failure-hunter`: diff/path plus changed files.

  Default: launch all three in one message. If `cenci.reviewConcurrency` is `"sequential"`, run the same reviewers one at a time in this order: security, code, silent-failure. Do not skip a reviewer.

The plan's `### Documentation Routing` section goes verbatim into every reviewer brief — each of the three on the `full` path and the single `code-reviewer` on `lite-small` — so a reviewer can check that documentation edits landed in the routing's owners.

## Verify Before Fixing

Before accepting reviews or acting on findings, apply the security and silent-failure
triggers in the plugin's `docs/model-escalation.md`. When triggered, delegate one
bounded second opinion to `domain-specialist`, including the normal reviewers'
findings and the relevant source paths. Preserve the reviewer set selected above,
review concurrency, and all fix/retry limits. Validate specialist findings with the
same source-reading rule below; do not treat model choice as evidence.

A reviewer's finding is a claim, not evidence — per Anthropic's Opus 5.5 guidance for subagent fan-out, when a subagent reports back, check its evidence before you accept it. Before delegating or applying any fix for a **Must Fix**, **Critical**, or **High** finding from any of the three reviewers, the orchestrating agent reads the cited file and line itself and confirms the finding holds against the actual code (the defect is really there, in the form claimed). A finding that does not hold is **not** fixed: record it as a one-line `Considered and discarded` entry in the PR's `## Notes` (see `phase-9-pr.md`) stating what was claimed and why it does not hold, so the rejection stays visible rather than silently vanishing. Findings below those tiers follow the fix-now-or-discard rules in the Actions sections below unchanged; this check adds no temp file, gate, or phase — it is a read of the cited location before the first fix is dispatched.

## Security Review Actions

The security reviewer checks OWASP, auth/authz, validation, injection, sensitive data, logging, and error exposure.

- Critical/High: confirm the finding first (see Verify Before Fixing), then fix immediately, run the fix's scoped Verification command (a fix report that lists a changed file marks the Completed-Change Verification evidence stale), rerun security review.
- Medium/Low: **Fix now** if the fix is straightforward. Otherwise, discard by default — test-coverage gaps, doc polish, "consider X" suggestions, and refactor/tech-debt observations are never tracked, only recorded as a one-line "Considered and discarded" entry in the PR's `## Notes` (see `phase-9-pr.md`), which never becomes a Followup ticket. **Track** it only when it is an actual defect with a concrete, realistic trigger path a user can hit in real use by noting it in the PR's `## Notes` for the Followup Ticket step.
- Unclear fix: ask the user via `AskUserQuestion`.

Security-critical findings take priority over code quality findings.

## Code Review Actions

The code reviewer uses confidence scoring and reports only findings >= 50.

- Must Fix >= 90: confirm each finding first (see Verify Before Fixing), then fix all that hold, run the fix's scoped Verification command (a fix report that lists a changed file marks the Completed-Change Verification evidence stale).
- Should Fix 75-89: **Fix now** if the fix is straightforward. Otherwise, discard by default — test-coverage gaps, doc polish, "consider X" suggestions, and refactor/tech-debt observations are never tracked, only recorded as a one-line "Considered and discarded" entry in the PR's `## Notes` (see `phase-9-pr.md`), which never becomes a Followup ticket. **Track** it only when it is an actual defect with a concrete, realistic trigger path a user can hit in real use by noting it in the PR's `## Notes` for the Followup Ticket step.
- Nitpicks 50-74: ignore unless trivial.
- Human decision: stop and ask the user via `AskUserQuestion`.

If `REQUEST_CHANGES`, delegate fixes to implementer, run the fix's scoped Verification command, and rerun code review. If the same issue persists after 2 fix attempts, escalate to the user.

After any fix-and-rerun cycle that changes the diff, re-run the Shared Context capture sequence (the `rm -f` reset, both exit-checked captures and the combine step) — writing into the same `$RUN_DIR` established at first entry, never calling `run-artifact-dir.sh` again — and re-evaluate the Review Path Classification against the *current* diff before re-invoking any reviewer. If the recomputed path is now **full** (e.g. the fix touched a force-full path, added a source file, or pushed changed lines over threshold) but the original run only launched `code-reviewer` (`lite-small`) or no reviewers (`lite-docs`), launch `security-reviewer` and `silent-failure-hunter` for the first time before proceeding — never leave a widened diff under-reviewed just because the original classification was narrower.

## Silent Failure Actions

The silent-failure hunter checks for swallowed errors, empty catch blocks, silent fallbacks, and missing error propagation.

- Critical in auth/payment/data-loss paths: confirm the finding first (see Verify Before Fixing), then fix immediately, run the fix's scoped Verification command (a fix report that lists a changed file marks the Completed-Change Verification evidence stale).
- Warning in non-critical paths: **Fix now** if the fix is straightforward. Otherwise, discard by default — test-coverage gaps, doc polish, "consider X" suggestions, and refactor/tech-debt observations are never tracked, only recorded as a one-line "Considered and discarded" entry in the PR's `## Notes` (see `phase-9-pr.md`), which never becomes a Followup ticket. **Track** it only when it is an actual defect with a concrete, realistic trigger path a user can hit in real use by noting it in the PR's `## Notes` for the Followup Ticket step.
- Info with intentional suppression and comments: no action.

## Completed-Change Verification Re-run

Phase 5's `## Completed-Change Verification` (`phases/phase-5-refactor.md`) is the one authoritative full check, and its evidence is session-local. Every fix round above (acceptance, security, code, silent-failure) runs only its fix's scoped Verification command and never the full suite. A fix round is a fresh delegation; when its report lists a changed file, it marks the evidence stale. A round whose report lists no changed file leaves the evidence valid and triggers no run. A report that is ambiguous or missing counts as stale (fail closed). Every fix-delegation report must carry a literal `Changed files:` line (a file list, or `Changed files: none`); a report without that line, and any edit the root agent applies itself, counts as stale.

When the evidence is stale, run Completed-Change Verification once, after the last fix round — never once per round — as a fresh delegation (in lane mode with the barrier's authority to touch any file, per Phase 5's `### Lane mode: Lane Verification Barrier`). Do not enter Phase 8 until it passes; its 3-attempt hard gate and diagnosis trigger apply unchanged. If this run changed any file, re-capture the Shared Context artifacts and apply the re-review rule above (re-classify the review path and re-review the new diff), bounded by the existing limit of 2 fix attempts before escalation. The final green run covers its own fixes.

A resumed run that reaches this phase without any fix delegation in the session runs none: the `reviewed`/`finalized` resume reaches Phase 9's post-rebase full run, which is the full check. Phase 8 documentation edits are likewise covered by Phase 9, with no extra run.
