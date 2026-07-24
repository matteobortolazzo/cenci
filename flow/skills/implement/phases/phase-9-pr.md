# Phase 9: Create PR

Read this file only when Phase 9 starts.

This phase is pre-approved — commit, push, and create the PR without asking for confirmation. The only exceptions are the error cases defined below (rebase conflicts, build/test/lint failures after rebase, push auth/network failures).

**Goal Autopilot**: if a goal was armed at Phase 2 (see `SKILL.md` → Goal Autopilot), it must be cleared here. Clear it on success (right after the PR is created, before plan-file cleanup) **and** before any of this phase's error gates hands control back to the user — an un-cleared goal restarts the turn and would loop on an unrecoverable state (a rebase conflict, a failed push). Run `/goal clear` (via the `SlashCommand` tool) at those points; it is a safe no-op if no goal was armed or `/goal` is unavailable.

**Atomicity rule — always restart at Rebase.** Any (re)entry into this phase — a fresh run or a Goal Autopilot resume mid-phase — MUST restart at the Rebase step below and run the steps in order: Rebase → build → test → lint → Commit → Push → PR. Never resume directly at Commit, Push, or PR creation, even if a prior turn already reached one of those steps. This guarantees push/PR creation is always immediately preceded, in the same turn, by a passing rebase + build + test + lint on the current tree — main may have moved between turns, and a stale rebase/verify from an earlier turn cannot be trusted. This is a stateless, markdown-level rule (no marker file, no new state): rebase and re-verify are cheap and idempotent, so restarting from the top on every entry is always safe. The Commit step below handles the case where a prior turn already committed (nothing left to stage) so the restart cannot double-commit or error out.

Prerequisites: all required reviews complete, Must Fix/Critical/High items resolved, build, tests, and lint pass.

Read `<worktree-path>/docs/git-workflow.md`; if absent, read legacy `<worktree-path>/.claude/rules/git-workflow.md`.

Source `ticketId`, `slug`, `isChild`, `isLastChild`, and `parentId` from plan front matter when `hasPlanFile` is true.

Target the worktree explicitly with `git -C <worktree-path>` on every rebase/commit/push command below so they resolve against the worktree and stay auto-approved — never compound a standalone `cd <worktree-path>` with the git command itself.

## Rebase

Fetch and rebase:

```bash
git -C <worktree-path> fetch origin main
git -C <worktree-path> rebase origin/main
```

If rebase succeeds, rerun full build and tests, then lint (when `lintCommand` is set). An absent `lintCommand` skips the lint step cleanly — no error. If build, tests, or lint fail, clear the goal (`/goal clear`), then stop and report the rebase-induced failure. Lint is an unconditional hard gate here, exactly like build/test: no PR is created if it fails.

If rebase conflicts, abort, clear the goal (`/goal clear`), report conflicting files, and stop:

```bash
git -C <worktree-path> rebase --abort
```

Tell the user to resolve manually. Per the atomicity rule above, the next entry into this phase still restarts at the Rebase step, not at Commit — the fetch+rebase is a no-op once the user has already resolved and completed it locally, and this guarantees a fresh build/test/lint pass on the rebased tree before Commit runs.

## Commit

Stage and commit:

```bash
git -C <worktree-path> add -A
git -C <worktree-path> commit -m "<type>(<scope>): <description>

<body if needed>

<ticket-ref>"
```

If a prior turn already committed this work (e.g. a Goal Autopilot resume re-entering after Commit ran once), `git -C <worktree-path> add -A` stages nothing new and `git -C <worktree-path> commit` reports nothing to commit — that is expected, not an error. Skip the commit in that case and proceed to Push; do not create an empty commit or fail the phase over it.

Ticket mode:

- Normal child/non-child: `Fixes #<childId or ticketId>`.
- Last child: include both `Fixes #<childId>` and `Fixes #<parentId>`.

Ticketless mode: no ticket reference.

## Push

Push the branch:

- Ticket mode: `git -C <worktree-path> push -u origin feature/<ticket-id>-<description>`
- Ticketless mode: `git -C <worktree-path> push -u origin feature/<auto-slug>`

If this branch was already pushed in a prior turn (a Goal Autopilot resume) and the atomicity rule's mandatory Rebase restart above rewrote local commit SHAs, the plain push above is rejected as non-fast-forward — this is expected, not a failure. Retry once with `git -C <worktree-path> push --force-with-lease -u origin <branch>`: `--force-with-lease` still refuses if the remote tip isn't what this rebase started from (i.e. someone else pushed to the branch), which surfaces as a genuine conflict to report rather than silently overwriting work.

If push fails due to sandbox/network/auth, clear the goal (`/goal clear`), show the exact command, and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.

After a successful push (ticket mode only — ticketless mode has no ticket ID to key the artifact on), record the branch as a tracked artifact:

```bash
cenci pipeline artifact <id> --branch <branch-name>
```

## Screenshots (UI Work)

Skip this section unless `isUiTicket` is true.

Screenshots are temporary review aids — never commit them to the repo. Host them in a **secret GitHub gist**: it lives under the user's account, is unlisted, and is disposable (`gh gist delete <gist-id>` after merge).

1. **Collect**: use the images Phase 4 persisted to `/tmp/claude/cenci-screenshots/<ticket-id-or-slug>/`. If the directory is missing or empty and `playwright-cli` is available, capture the affected screens now against the dev build. If capture is not possible, skip the upload and use the fallback body in step 5.
2. **Privacy check**: secret gists are unlisted but readable by anyone with the URL. Screenshots must show only local/dev data — no real user data, tokens, or internal URLs. Crop or re-capture rather than upload.
3. **Create the gist** (gists require a text file at creation; images are pushed via git afterwards):

   ```bash
   printf 'Screenshots for %s — temporary, delete after merge.\n' "<branch>" > /tmp/claude/cenci-screenshots/<ticket-id-or-slug>-README.md
   gh gist create --desc "cenci screenshots: <owner/repo> <branch>" /tmp/claude/cenci-screenshots/<ticket-id-or-slug>-README.md
   ```

   The command prints the gist URL; extract `<gist-id>` from it. Do not pass `--public` — the gist must stay secret.
4. **Push the images** through the gist's git remote (no `cd` compounds — see the `shell-rules` skill):

   ```bash
   gh gist clone <gist-id> /tmp/claude/cenci-gist-<gist-id>
   cp /tmp/claude/cenci-screenshots/<ticket-id-or-slug>/*.png /tmp/claude/cenci-gist-<gist-id>/
   git -C /tmp/claude/cenci-gist-<gist-id> add -A
   git -C /tmp/claude/cenci-gist-<gist-id> commit -m "PR screenshots"
   git -C /tmp/claude/cenci-gist-<gist-id> push
   ```
5. **Build embed URLs**: `https://gist.githubusercontent.com/<gh-user>/<gist-id>/raw/<filename>.png`, where `<gh-user>` comes from `gh api user -q .login`. These go into the PR body's `## Screenshots` section (template below). If any gist step fails (auth, network), do not block PR creation — write `## Screenshots` with "Not uploaded (<reason>); local copies at `/tmp/claude/cenci-screenshots/<ticket-id-or-slug>/`" instead.

## PR

Create the PR with `gh pr create`. Write body content to `/tmp/claude/cenci-<ticket-id-or-slug>-pr-body.md` first and read it back; do not use heredocs or a large inline body string.

If a prior turn already created the PR (a Goal Autopilot resume re-entering after PR creation ran once but the turn ended before `/goal clear`), `gh pr create` fails with "a pull request for branch ... already exists." That is not a failure — run `gh pr view <branch> --json url,number -q '.url + " " + (.number | tostring)'` to recover the existing PR URL and number, and continue to Labels/Cleanup as if creation had just succeeded.

If `gh pr create` fails for any other reason (auth, network, validation), clear the goal (`/goal clear`), show the exact failing command and its error output, and use `AskUserQuestion` ("Created, continue" / "Abort") to let the user resolve the issue and confirm before continuing to Labels/Cleanup, or abort the run — mirroring the Push gate above. On "Created, continue," re-run `gh pr view <branch> --json url,number -q '.url + " " + (.number | tostring)'` to obtain the PR URL/number before proceeding — the same recovery call as the "already exists" case above — since Labels/Cleanup and the Followup Ticket step below need it.

However the PR URL/number was obtained above (fresh `gh pr create`, or either recovery path), record it as a tracked artifact (ticket mode only):

```bash
cenci pipeline artifact <id> --pr <pr-url> --pr-number <pr-number>
```

Ticket mode body includes:

```markdown
## Summary
<1-2 sentences>

## Ticket
<Fixes/Related refs>

## Changes
- <change>

## Testing
<commands and results>

## Review
<Review line — see below>

## Screenshots
_Temporary secret gist, not part of the repo — delete after merge: `gh gist delete <gist-id>`_

### <screen or state name>
![<screen or state name>](https://gist.githubusercontent.com/<gh-user>/<gist-id>/raw/<filename>.png)

## Checklist
- [x] Tests pass
<Security checklist line — see below>
<Maintenance check checklist line — see below>
- [ ] Documentation updated

## Notes
<Tracked deferred items — Medium/Low security findings, deferred Should Fix items, deferred non-critical silent-failure warnings — or "None">

### Considered and discarded
<One line per discarded finding — what was found + why it was discarded — or "None">
```

For child tickets that are not last child, use `Related to #<parentId>` for the parent so it is not auto-closed. For ticketless mode, omit `## Ticket`.

`## Review` reports which Phase 6 + 7 path ran, sourced from `$RUN_DIR/review-path.txt` (written during Phase 6 + 7's Review Path Classification):

- `full` → "Review: full trio"
- `lite-docs` → "Review: lite (docs-only — no reviewers)"
- `lite-small` → "Review: lite (code-reviewer only)"

If `$RUN_DIR` is unknown (e.g. lost to a context compaction or a goal-autopilot resume in a fresh session that never re-ran Phase 6 + 7's Shared Context step) or the file at that path is absent, do not default to any of the three known paths — write "Review: unknown (RUN_DIR lost — could not determine which review path ran)". An unrecoverable path must never be silently reported as `full`, `lite-docs`, or `lite-small`: claiming `full` would be a false assurance (the actual run could have been `lite-docs`, with no reviewers at all), so the PR body must surface the gap honestly instead of guessing.

The `## Checklist` security line is derived from the same `$RUN_DIR/review-path.txt` file, in the same read:

- `full` → `- [x] Security review done`
- `lite-docs` or `lite-small` → `- [ ] Security review skipped (see Review section — <path>)`, where `<path>` is the literal path value (`lite-docs` or `lite-small`)
- `$RUN_DIR` unknown or file absent → `- [ ] Security review status unknown (RUN_DIR lost — verify manually before merge)`, consistent with the "Review: unknown" fallback above — never claim a security review was done, or was skipped, when the actual path isn't known; an unverifiable state must read as unverifiable, not as either known outcome.

The `## Checklist` Maintenance check line is derived from `$RUN_DIR/maintain-status.txt` (written by Phase 8's Maintenance Check sub-step), read next to `review-path.txt` above:

- summary line has no trailing `— <tag>` at all (the all-clean case: zero non-pass results, nothing to repair/report/halt) → `- [x] Maintenance check passed`
- summary line shows `— repaired` and no `fail` → `- [x] Maintenance check passed (auto-repaired same-PR drift — see Notes)`
- summary line shows `— reported` → `- [ ] Maintenance check: findings reported (see Notes)`
- file's first line is `maintenance: skipped …` → `- [x] Maintenance check: not applicable`
- file's first line is `maintenance: error …` (the checker itself crashed with no `summary:` line — see Phase 8's checker-crash guard) → `- [ ] Maintenance check: error (checker execution failed — verify manually before merge)` — never render this as a pass; the checker never produced a real pass/fail summary to report on.
- `$RUN_DIR` unknown or the file is absent → `- [ ] Maintenance check status unknown (RUN_DIR lost — verify manually before merge)`, mirroring the Review/Security "RUN_DIR lost" honesty rule above.

Any reported or deferred maintenance findings (from a `— reported` status) each append one line to `## Notes`. The Cleanup step's `rm -rf "$RUN_DIR"` already removes `maintain-status.txt` with the rest of the run's artifacts — no new cleanup line is needed.

`## Screenshots` appears only when `isUiTicket` is true: one `### <name>` + image per captured screen/state, or the fallback note from the Screenshots section above. Omit the section entirely for non-UI work. If the user chose "Proceed without design" at the Design Check, add "Implemented without design spec — extra visual review recommended." to `## Notes`.

## Labels

Ticket mode: after PR creation, replace "Working" with "In Review" (the PR is open but not yet merged):

```bash
cenci pipeline label <id> --transition in-review
```

Render the returned `state`/`next_actions`/`warnings`/`errors`. The CLI self-heals `In Review`'s existence in the repository and treats "already exists" as success, so no separate self-heal call is needed.

If `isLastChild`, pass `--parent <parentId>` on the same call so the CLI also cascades "In Review" to the parent ticket:

```bash
cenci pipeline label <id> --transition in-review --parent <parentId>
```

The parent's real completion — the transition to "Implemented" — arrives when this last child's PR merges: the last-child commit carries `Fixes #<parentId>` (see Commit above), so the parent appears in the PR's `closingIssuesReferences` and babysit relabels it on merge.

The `Working` → `In Review` → `Implemented` progression finishes on merge: babysit swaps `In Review` for `Implemented` on any issue closed by the merged PR (see the babysit skill's terminal check). PR-open never applies `Implemented`.

## Followup Ticket

The `## Notes` section above — **excluding its `### Considered and discarded` subsection** — is the sole source of tracked/deferred items (deferred Should Fix items, Medium/Low security findings, deferred non-critical silent-failure warnings). Entries under `### Considered and discarded` are recorded for review visibility only and never feed Followup ticket creation.

If there is **nothing** to capture (no tracked `## Notes` items — entries under `### Considered and discarded` do not count), create no ticket and skip this section entirely.

If ≥1 deferred item exists, ensure the label exists (its own Bash call — note `2>/dev/null || true` suppresses **every** failure, not just "already exists"; a genuine failure (auth, network, permissions) surfaces on the next command as a "label not found" error from `gh issue create` — treat that as the label-create failure it is):

```bash
gh label create "Followup" --repo <owner>/<repo> --color "C5DEF5" --description "Deferred/out-of-scope item captured from a session — triage before working" 2>/dev/null || true
```

Ticket mode only: before creating the follow-up issue, fetch the original ticket's milestone and labels so the follow-up can inherit them. Run this as its own Bash call so a fetch failure surfaces distinctly, before the create call below:

```bash
gh issue view <original-ticket> --repo <owner>/<repo> --json milestone,labels > /tmp/claude/cenci-<ticket-id-or-slug>-followup-meta.json || rm -f /tmp/claude/cenci-<ticket-id-or-slug>-followup-meta.json
```

Ticketless mode skips this fetch entirely — same as it already omits `Related to #<original-ticket>` below.

### Generation limit — no followup from a followup

In ticket mode, reuse the `followup-meta.json` fetched just above (zero new `gh` calls) to decide whether this run may mint a followup at all: read `.labels[].name` and, if it includes `Followup`, then the ticket being implemented in this run is itself a Followup ticket — set `SKIP_FOLLOWUP_CREATE=true` and skip the dedup search, the create block, and the original-ticket comment below entirely, so a Followup ticket's own PR never spawns another Followup; the surviving `## Notes` items stay in this PR's body (already written above) and are simply not re-tracked, and the final session summary notes that followup creation was skipped because this run's own ticket is a Followup.

If the meta fetch failed in ticket mode (no `followup-meta.json` present even though an original ticket exists), also set `SKIP_FOLLOWUP_CREATE=true` and skip — not as a graceful degrade: create no new followup ticket, fail-closed, because an unreadable label list cannot rule out a Followup origin, and re-tracking a followup's deferrals into a fresh followup is exactly the generation chain this limit exists to stop (unlike the cosmetic milestone/label inheritance degrade above, whose worst case is only a missing inherited label). Ticketless mode is unchanged: it has no original ticket and no lineage to extend, so the generation limit does not apply and creation proceeds as before.

### Dedup before create — append to an existing open Followup

Run this step (and the create block below, and the comment) only when `SKIP_FOLLOWUP_CREATE` is unset. In ticket mode, before minting anything, search the open Followup backlog for a ticket already tracking this run's original ticket and append to it instead of opening a duplicate:

```bash
gh issue list --repo <owner>/<repo> --label "Followup" --state open --json number,body > /tmp/claude/cenci-<ticket-id-or-slug>-followup-search.json
```

Set `MATCH_N` to the lowest-numbered open issue whose `body` contains the literal substring `Related to #<original-ticket>` — the exact back-link line this section writes below, `<original-ticket>` being this run's own ticket ID. This must match on the literal body substring only; never match on title similarity or any fuzzy heuristic — the back-link is the sole join key, so an unrelated ticket that merely shares words in its title is never treated as a duplicate. If no open issue contains that exact line (or in ticketless mode, which has no back-link to search on), there is no `MATCH_N`: fall through to the create block below unchanged.

If `MATCH_N` is set, append this run's deferred items to it instead of creating a new ticket. Re-read the matched issue's current body, then form the new checklist lines in the same one-line-context + `<file/area>` format as the create body below. Apply a resume-safe idempotency guard against a Goal Autopilot re-entry double-appending: `grep -qF` each candidate line against the existing body and drop any already present; if nothing new remains, skip the edit entirely rather than pushing an empty change. Otherwise write the full updated body — the existing body with the surviving new lines appended under its existing `## Deferred Items` checklist — to `/tmp/claude/cenci-<ticket-id-or-slug>-followup-body.md`, re-applying no label or milestone (the matched ticket already carries them):

```bash
gh issue view "$MATCH_N" --repo <owner>/<repo> --json body -q .body > /tmp/claude/cenci-<ticket-id-or-slug>-followup-existing-body.md
gh issue edit "$MATCH_N" --repo <owner>/<repo> --body-file /tmp/claude/cenci-<ticket-id-or-slug>-followup-body.md
```

On an append, set `<n>` = `$MATCH_N` for the original-ticket comment below and skip the create block entirely — the comment still fires so the original ticket links to the followup that now carries its items.

### Create the followup ticket

Reach this block only when `SKIP_FOLLOWUP_CREATE` is unset **and** no `MATCH_N` was found (or in ticketless mode). Write the body to `/tmp/claude/cenci-<ticket-id-or-slug>-followup-body.md` with the file tool — and the title too, to `/tmp/claude/cenci-<ticket-id-or-slug>-followup-title.txt`: the PR title is free text and must never be interpolated directly into the command line (a title containing `$(…)`, backticks, or quotes would be shell-interpreted). Then create the ticket in one call, reading the title (and, when the fetch above succeeded, the inherited milestone/labels) back the same way Posting Replies in `address-review` reads reply text. Labels and the milestone are externally-sourced free text, so they are passed as array args (`--label "$l"` per label, `--milestone "$MILESTONE"`), never inline-interpolated into the command string. Carry over every original label except the 7 lifecycle/transient markers — `"Refined","Working","Planned","In Review","Implemented","Design","Designed"` — and `Followup` itself (which is always applied on top regardless of what's carried over); the milestone is applied only when the original ticket actually has one, via an explicit jq emptiness check rather than a bare `//` fallback (see `docs/shell-scripting-gotchas.md`):

```bash
TITLE=$(cat /tmp/claude/cenci-<ticket-id-or-slug>-followup-title.txt) || { echo "followup title read failed" >&2; exit 1; }
LABEL_ARGS=(--label "Followup")
MILESTONE_ARGS=()
if [[ -f /tmp/claude/cenci-<ticket-id-or-slug>-followup-meta.json ]]; then
  MILESTONE=$(jq -r 'if (.milestone.title // "") != "" then .milestone.title else empty end' /tmp/claude/cenci-<ticket-id-or-slug>-followup-meta.json)
  mapfile -t CARRIED < <(jq -r '.labels[].name | select(. as $n | (["Refined","Working","Planned","In Review","Implemented","Design","Designed","Followup"] | index($n)) | not)' /tmp/claude/cenci-<ticket-id-or-slug>-followup-meta.json)
  for l in "${CARRIED[@]}"; do LABEL_ARGS+=(--label "$l"); done
  [[ -n "$MILESTONE" ]] && MILESTONE_ARGS=(--milestone "$MILESTONE")
fi
gh issue create --repo <owner>/<repo> --title "$TITLE" \
  "${LABEL_ARGS[@]}" "${MILESTONE_ARGS[@]}" \
  --body-file /tmp/claude/cenci-<ticket-id-or-slug>-followup-body.md
```

Ticketless mode falls through this same command unchanged: `LABEL_ARGS`/`MILESTONE_ARGS` stay at their defaults and the issue is created with `--label "Followup"` only — ticketless has no original ticket, so there is nothing to inherit and no lineage to check. Ticket mode reaches this block only after a *successful* meta fetch (a failed fetch was already handled fail-closed by the generation limit above, which skipped creation), so inheritance is a visibility enhancement, not a correctness gate; if that successful fetch nonetheless returned an empty milestone and no carry-over labels, note in the final session summary that milestone/label inheritance was skipped so the gap is visible.

Body content (checklist of items, each with a one-line context and file/area reference):

```markdown
## Deferred Items
- [ ] <one-line context> — `<file/area>`

Related to #<original-ticket>

PR: <PR URL>
```

Ticket mode: include the `Related to #<original-ticket>` line — `<original-ticket>` is this run's own ticket ID (for a child ticket, the child; the parent is already linked via the commit's `Fixes #<parentId>` on last-child PRs). Ticketless mode: omit it, keep the PR link. The followup ticket does **not** receive the `Refined` label — it enters the backlog unrefined; a human triages it and runs `/cenci:refine` when it's worth doing.

Assume the issue is world-readable: never transcribe secret values, credentials, or exploitable vulnerability detail into the body. Reference deferred security findings abstractly — one neutral line plus the file/area, not the finding's specifics.

If `gh issue create` fails, or no issue number can be parsed from its output URL, do **not** fabricate `<n>` and do not post any text referencing it — skip the comment below and report the exact error (with the deferred-item list) in the final session summary so the items aren't silently lost.

Ticket mode only, and only when `SKIP_FOLLOWUP_CREATE` is unset: after the followup issue is created successfully — or after this run appended to an existing `MATCH_N` above — comment on the original ticket (this run's ticket ID) with the followup ticket number `<n>` (parsed from a fresh create's output URL, or `$MATCH_N` for an append):

```bash
gh issue comment <original-number> --repo <owner>/<repo> --body "Followups tracked in #<n>"
```

Ticketless mode: skip this comment (there is no original ticket to comment on).

## Cleanup

After successful PR creation, first clear the Goal Autopilot condition — the PR now exists, so the goal is met:

```
/goal clear
```

Run it via the `SlashCommand` tool; it is a no-op if no goal was armed. Clearing before the plan-file deletion below keeps the two "work is done" signals in sync: the goal's condition references `.plans/<filename>`, which is removed next.

Then delete the consumed plan file. If `.plans/` is empty, remove it. If the pipeline fails before PR creation, preserve the plan file for retry (and, as above, clear the goal at whichever error gate stopped the run).

Finally, delete this run's scoped shared temp files — they were only ever intermediate state for this ticket's pipeline run, and the PR now carries everything they contributed. Phase 6 + 7's four review artifacts (diff patch, changed-file list, stat, review-path) live together in `$RUN_DIR`, so remove the whole directory in one step, guarded on `RUN_DIR` actually being known:

```bash
[[ -n "${RUN_DIR:-}" && -d "$RUN_DIR" ]] && rm -rf "$RUN_DIR"
```

If `$RUN_DIR` is unknown (lost to compaction — see the `## Review`/`## Checklist` fallback above), skip this step; the directory becomes a small, acceptable ephemeral `/tmp` leak rather than a risk of deleting the wrong path.

The remaining per-ticket temp files stay ticket-slug-scoped and out of this ticket's scope:

```bash
rm -f \
  /tmp/claude/cenci-<ticket-id-or-slug>-pr-body.md \
  /tmp/claude/cenci-<ticket-id-or-slug>-followup-title.txt \
  /tmp/claude/cenci-<ticket-id-or-slug>-followup-body.md \
  /tmp/claude/cenci-<ticket-id-or-slug>-followup-meta.json \
  /tmp/claude/cenci-<ticket-id-or-slug>-followup-search.json \
  /tmp/claude/cenci-<ticket-id-or-slug>-followup-existing-body.md \
  /tmp/claude/cenci-<ticket-id-or-slug>-explore-1.md \
  /tmp/claude/cenci-<ticket-id-or-slug>-explore-2.md \
  /tmp/claude/cenci-context-<ticket-id-or-slug>.md
```

This deliberately excludes two other scoped temp locations: the screenshots dir (`/tmp/claude/cenci-screenshots/<ticket-id-or-slug>/`) is a documented fallback location kept for gist-upload failures (see Screenshots above), and the gist clone temp dir (`/tmp/claude/cenci-gist-<gist-id>/`) is already uniquely scoped by gist ID — neither needs this pass to stay collision-safe.

Like the plan-file deletion above, this cleanup only runs on the success path (PR created). If the pipeline fails before PR creation, these files are preserved for retry/debugging, same as the plan file.

## Babysit

This is the **final** Phase-9 action — do it only after `## Cleanup` above has settled the session's own completion (`/goal clear` ran, the plan file was deleted). Ordering matters: this step arms an *unattended* `cenci babysit` supervisor that keeps running after this session exits, so per `flow/AGENTS.md` the goal-clear-before-handoff rule is restated here for that new "arms an unattended loop" risk profile — the goal must already be cleared (Cleanup) before the supervisor is launched, so a still-armed goal can never re-loop the turn *after* an external watcher is live.

The PR is open but unverified — CI has not run, review feedback has not arrived, and the ticket is still `In Review`, not `Implemented`. Hand the PR off to the persistent supervisor, which carries it the rest of the way (CI watch, review handling, and the final `In Review` → `Implemented` relabel on merge) exactly as the standalone `babysit` skill does when invoked by hand. `cenci babysit` detaches its own background supervisor and returns immediately, so this launch is non-blocking — the session ends here; babysit runs on.

**Skip entirely in ticketless mode** — there is no PR number tracked as an artifact in ticketless mode; the babysit hand-off is a ticket-mode step, like the Labels and artifact calls above. (A ticketless run still opened a real PR; the user can `cenci babysit <pr> <interval>` it manually via the `babysit` skill.)

1. **Resolve the interval.** The watch cadence is the optional `babysitInterval` field in `.cenci/config.json`, resolved with the shared resolver — top-level for a single-repo, or the affected project's value in a monorepo. Pass the single affected project slug when one was resolved at Phase 2's Baseline Gate Check (the same `projects[].slug` used for the baseline gate); otherwise resolve top-level with no slug argument:

   ```bash
   ( cd "<abs-worktree-path>" && sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh" "<slug>" )
   ```

   Omit the trailing `"<slug>"` for the single-repo / top-level case. Non-empty stdout → use it as `<interval>` below; empty stdout (field unset, or no config file) → omit `--interval` entirely and let `cenci babysit` use its built-in `15m` default. A non-zero exit with a stderr diagnostic (missing `jq`, malformed config, no-match/ambiguous slug) is **not** fatal here: report it and fall back to the default by omitting `--interval` — a bad interval lookup must never block arming the watcher.

2. **Launch the supervisor** for the PR number already captured for the `cenci pipeline artifact <id> --pr-number` call above:

   ```bash
   cenci babysit <pr-number> --agent claude --interval <interval>
   ```

   Drop `--interval <interval>` when step 1 resolved nothing.

3. **Non-fatal & idempotent.** If the launch prints `supervisor already running for PR #<pr-number>`, that is **expected success**, not an error — a Goal-Autopilot re-entry (or a manual re-run) already armed the watcher, and `cenci babysit` refuses to start a second supervisor for the same PR. Treat it exactly like the "PR already exists" / "label already exists" cases earlier in this phase: proceed to the report below. Any *other* launch failure (auth, network, missing binary) is reported to the user but does **not** fail the phase or re-loop the goal — the PR already exists and is the pipeline's real deliverable; a failed watcher launch just means the user should arm it manually.

Finally, report the terminal state as the PR being open and watched, not as done/merged:

> PR #<pr-number> open and being watched by babysit → <pr-url> (stop with `cenci babysit stop <pr-number>`)
