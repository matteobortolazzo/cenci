# Phase 9: Create PR

Read this file only when Phase 9 starts.

This phase is pre-approved — commit, push, and create the PR without asking for confirmation. The only exceptions are the error cases defined below (rebase conflicts, build/test/lint failures after rebase, push auth/network failures).

**Atomicity rule — always restart at Rebase.** Any (re)entry into this phase — a fresh run, or a re-run after an earlier attempt stopped mid-phase — MUST restart at the Rebase step below and run the steps in order: Rebase → build → test → lint → Commit → Push → PR. Never resume directly at Commit, Push, or PR creation, even if a prior turn already reached one of those steps. This guarantees push/PR creation is always immediately preceded, in the same turn, by a passing rebase + build + test + lint on the current tree — main may have moved between turns, and a stale rebase/verify from an earlier turn cannot be trusted. This is a stateless, markdown-level rule (no marker file, no new state): rebase and re-verify are cheap and idempotent, so restarting from the top on every entry is always safe. The Commit step below handles the case where a prior turn already committed (nothing left to stage) so the restart cannot double-commit or error out.

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

If rebase succeeds, rerun full build and tests, then lint (when `lintCommand` is set). An absent `lintCommand` skips the lint step cleanly — no error. If build, tests, or lint fail, stop and report the rebase-induced failure. Lint is an unconditional hard gate here, exactly like build/test: no PR is created if it fails.

If rebase conflicts, abort, report conflicting files, and stop:

```bash
git -C <worktree-path> rebase --abort
```

Tell the user to resolve manually. Per the atomicity rule above, the next entry into this phase still restarts at the Rebase step, not at Commit — the fetch+rebase is a no-op once the user has already resolved and completed it locally, and this guarantees a fresh build/test/lint pass on the rebased tree before Commit runs.

## Parent Close Gate (last child only)

Skip this section entirely unless `isLastChild` is true. It produces a single verdict — `close` or `hold` — consumed by the Commit trailer, the PR body's parent reference, and the Labels `--parent` cascade below. `isLastChild` is a graph-topology signal (zero open siblings), never proof the parent is done (#661): a parent ticket may only be closed when its acceptance criteria are verifiably met, so the parent-closing trailer is gated on this audit's verdict. Re-entry under the atomicity rule re-runs the gate like every other step — the audit is read-only against GitHub plus this worktree's diff, so re-running it is safe and cheap, and a re-run may legitimately flip an earlier verdict if the tree changed.

1. **Fetch the parent's body** to a file (the `|| rm -f` guard mirrors the followup-meta fetch below — a partially-written file must never pass an existence check):

   ```bash
   gh issue view <parentId> --repo <owner>/<repo> --json body --jq .body > ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-parent-body.md || rm -f ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-parent-body.md
   ```

   If the fetch fails, retry once; if it still fails, the verdict is `hold` — an unreadable parent can never be proven complete (fail-closed) — and the final session summary must report that the parent audit could not run.

2. **Extract the criteria.** `Read` the fetched body and collect the `- [ ]`/`- [x]` items under its `### Acceptance Criteria` heading. If the parent body has no `### Acceptance Criteria` section, there is nothing to audit: the verdict is `close`, and the final session summary must note that parent #`<parentId>` closed without an acceptance-criteria audit (the parent carries no such section).

3. **Audit every criterion** — checked and unchecked alike; checkbox state on the parent is not maintained by the pipeline and proves nothing. A criterion counts as **met** only with concrete, citable evidence: a specific change or test in this worktree's diff, or a specific merged sibling PR. Locate siblings via `gh issue view <parentId> --repo <owner>/<repo> --json subIssues` and map each closed sibling to its merged PR (its `Fixes #<sibling>` reference; `gh pr list --repo <owner>/<repo> --state merged --search "<siblingId>"` when needed). A criterion with no evidence, or whose evidence you cannot pin to a citation, counts as **unmet** — uncertainty is a `hold`, never a guess into the met column.

4. **Verdict.** Every criterion met → `close`: the Commit, PR, and Labels steps below take their `close` branches. One or more unmet (or the fetch failure above) → `hold`:
   - Use the `Write` tool to create the gap report at `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-parent-gaps.md` — one line per unmet criterion with a one-line reason it is unmet, opening with a sentence that this last-child PR deliberately did not close the parent — then post it on the parent:

     ```bash
     gh issue comment <parentId> --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-parent-gaps.md
     ```

     If the comment fails after one retry, keep the `hold` verdict regardless (the downgrade below is the safety mechanism; the comment is only its visibility) and report the failed comment together with the gap list in the final session summary so the gaps are never silently lost.
   - The parent stays open for human triage — typically a re-refine of the remaining gaps into new children — and the final session summary reports that parent #`<parentId>` was left open, listing the unmet criteria.

## Commit

Stage and commit:

```bash
git -C <worktree-path> add -A
git -C <worktree-path> commit -m "<type>(<scope>): <description>

<body if needed>

<ticket-ref>"
```

If an earlier attempt already committed this work (a re-run re-entering after Commit ran once), `git -C <worktree-path> add -A` stages nothing new and `git -C <worktree-path> commit` reports nothing to commit — that is expected, not an error. Skip the commit in that case and proceed to Push; do not create an empty commit or fail the phase over it.

Ticket mode:

- Normal child/non-child: `Fixes #<childId or ticketId>`.
- Last child with a `close` verdict from the Parent Close Gate: `Fixes #<childId>` plus `Fixes #<parentId>` — the parent-closing trailer is written only on a `close` verdict, never from `isLastChild` alone.
- Last child with a `hold` verdict: `Fixes #<childId>` plus `Related to #<parentId>` — the parent stays open; its unmet criteria were posted on the parent by the gate.

Ticketless mode: no ticket reference.

## Push

Push the branch:

- Ticket mode: source the branch to push from the recorded pipeline artifact rather than reconstructing it — `cenci pipeline artifact <id> --get` returns the frozen `{state, next_actions, artifacts, warnings, errors}` contract; its `branch` value is one of the self-describing `key:value` entries in `artifacts[]`, extracted with:

  ```bash
  BRANCH=$(cenci pipeline artifact <id> --get | jq -r '.artifacts[] | select(startswith("branch:")) | sub("^branch:";"")')
  ```

  This is `feature/<ticket-id>-<description>` on the standard path, or a non-standard branch when this run reused an existing worktree via `cenci pipeline worktree <id> --attach <path>` at Phase 2 — see `phase-2-worktree.md`'s `## Create Worktree`. Then push that branch: `git -C <worktree-path> push -u origin "$BRANCH"`.
- Ticketless mode: `git -C <worktree-path> push -u origin feature/<auto-slug>` — unchanged; ticketless mode has no pipeline artifact to source a branch from.

If this branch was already pushed by an earlier attempt and the atomicity rule's mandatory Rebase restart above rewrote local commit SHAs, the plain push above is rejected as non-fast-forward — this is expected, not a failure. Retry once with `git -C <worktree-path> push --force-with-lease -u origin <branch>`: `--force-with-lease` still refuses if the remote tip isn't what this rebase started from (i.e. someone else pushed to the branch), which surfaces as a genuine conflict to report rather than silently overwriting work.

If push fails due to sandbox/network/auth, show the exact command, and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.

After a successful push (ticket mode only — ticketless mode has no ticket ID to key the artifact on), record the branch as a tracked artifact:

```bash
cenci pipeline artifact <id> --branch <branch-name>
```

## Screenshots (UI Work)

Skip this section unless `isUiTicket` is true.

Screenshots are temporary review aids — never commit them to the repo. Host them in a **secret GitHub gist**: it lives under the user's account, is unlisted, and is disposable (`gh gist delete <gist-id>` after merge).

1. **Collect**: use the images Phase 4 persisted to `${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/`. If the directory is missing or empty and `playwright-cli` is available, capture the affected screens now against the dev build. If capture is not possible, skip the upload and use the fallback body in step 5.
2. **Privacy check**: secret gists are unlisted but readable by anyone with the URL. Screenshots must show only local/dev data — no real user data, tokens, or internal URLs. Crop or re-capture rather than upload.
3. **Create the gist** (gists require a text file at creation; images are pushed via git afterwards):

   ```bash
   printf 'Screenshots for %s — temporary, delete after merge.\n' "<branch>" > ${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>-README.md
   gh gist create --desc "cenci screenshots: <owner/repo> <branch>" ${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>-README.md
   ```

   The command prints the gist URL; extract `<gist-id>` from it. Do not pass `--public` — the gist must stay secret.
4. **Push the images** through the gist's git remote (no `cd` compounds — see the `shell-rules` skill):

   ```bash
   gh gist clone <gist-id> ${TMPDIR:-/tmp}/cenci/cenci-gist-<gist-id>
   cp ${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/*.png ${TMPDIR:-/tmp}/cenci/cenci-gist-<gist-id>/
   git -C ${TMPDIR:-/tmp}/cenci/cenci-gist-<gist-id> add -A
   git -C ${TMPDIR:-/tmp}/cenci/cenci-gist-<gist-id> commit -m "PR screenshots"
   git -C ${TMPDIR:-/tmp}/cenci/cenci-gist-<gist-id> push
   ```
5. **Build embed URLs**: `https://gist.githubusercontent.com/<gh-user>/<gist-id>/raw/<filename>.png`, where `<gh-user>` comes from `gh api user -q .login`. These go into the PR body's `## Screenshots` section (template below). If any gist step fails (auth, network), do not block PR creation — write `## Screenshots` with "Not uploaded (<reason>); local copies at `${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/`" instead.

## PR

Create the PR with `gh pr create`. Write body content to `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-pr-body.md` first and read it back; do not use heredocs or a large inline body string.

If an earlier attempt already created the PR (a re-run re-entering after PR creation ran once), `gh pr create` fails with "a pull request for branch ... already exists." That is not a failure — run `gh pr view <branch> --json url,number -q '.url + " " + (.number | tostring)'` to recover the existing PR URL and number, and continue to Labels/Cleanup as if creation had just succeeded.

If `gh pr create` fails for any other reason (auth, network, validation), show the exact failing command and its error output, and use `AskUserQuestion` ("Created, continue" / "Abort") to let the user resolve the issue and confirm before continuing to Labels/Cleanup, or abort the run — mirroring the Push gate above. On "Created, continue," re-run `gh pr view <branch> --json url,number -q '.url + " " + (.number | tostring)'` to obtain the PR URL/number before proceeding — the same recovery call as the "already exists" case above — since Labels/Cleanup and the Followup Ticket step below need it.

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

For child tickets that are not last child — and for a last child whose Parent Close Gate verdict is `hold` — use `Related to #<parentId>` for the parent so it is not auto-closed. For ticketless mode, omit `## Ticket`.

`## Review` reports which Phase 6 + 7 path ran, sourced from `$RUN_DIR/review-path.txt` (written during Phase 6 + 7's Review Path Classification):

- `full` → "Review: full trio"
- `lite-docs` → "Review: lite (docs-only — no reviewers)"
- `lite-small` → "Review: lite (code-reviewer only)"

If `$RUN_DIR` is unknown (e.g. lost to a context compaction, or a re-run in a fresh session that never re-ran Phase 6 + 7's Shared Context step) or the file at that path is absent, do not default to any of the three known paths — write "Review: unknown (RUN_DIR lost — could not determine which review path ran)". An unrecoverable path must never be silently reported as `full`, `lite-docs`, or `lite-small`: claiming `full` would be a false assurance (the actual run could have been `lite-docs`, with no reviewers at all), so the PR body must surface the gap honestly instead of guessing.

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

Ticket mode: after PR creation, apply "In Review" and retire both working-lifecycle labels, "Working" and "Planned" (the PR is open but not yet merged, and the ticket is no longer queued for pickup):

```bash
cenci pipeline label <id> --transition in-review
```

Render the returned `state`/`next_actions`/`warnings`/`errors`. The CLI self-heals `In Review`'s existence in the repository and treats "already exists" as success, so no separate self-heal call is needed.

If `isLastChild` and the Parent Close Gate verdict is `close`, pass `--parent <parentId>` on the same call so the CLI also cascades "In Review" to the parent ticket:

```bash
cenci pipeline label <id> --transition in-review --parent <parentId>
```

On a `hold` verdict, omit `--parent` — the parent is not completing with this PR; it stays open with the gate's gap comment, outside this PR's label lifecycle.

The parent's real completion — the transition to "Implemented" — arrives when this last child's PR merges: on a `close` verdict the last-child commit carries `Fixes #<parentId>` (see Commit above), so the parent appears in the PR's `closingIssuesReferences` and babysit relabels it on merge.

The `Working` → `In Review` → `Implemented` progression finishes on merge: the `cenci babysit` supervisor swaps `In Review` for `Implemented` on any issue closed by the merged PR (see the babysit skill's Safety guarantees section). PR-open never applies `Implemented`.

## Followup Ticket

The `## Notes` section above — **excluding its `### Considered and discarded` subsection** — is the sole source of tracked/deferred items (deferred Should Fix items, Medium/Low security findings, deferred non-critical silent-failure warnings). Entries under `### Considered and discarded` are recorded for review visibility only and never feed Followup ticket creation.

If there is **nothing** to capture (no tracked `## Notes` items — entries under `### Considered and discarded` do not count), create no ticket and skip this section entirely.

If ≥1 deferred item exists, ensure the label exists (its own Bash call — note `2>/dev/null || true` suppresses **every** failure, not just "already exists"; a genuine failure (auth, network, permissions) surfaces on the next command as a "label not found" error from the followup create — treat that as the label-create failure it is):

```bash
gh label create "Followup" --repo <owner>/<repo> --color "C5DEF5" --description "Deferred/out-of-scope item captured from a session — triage before working" 2>/dev/null || true
```

Ticket mode only: before creating the follow-up issue, fetch the original ticket's milestone and labels so the follow-up can inherit them. Run this as its own Bash call so a fetch failure surfaces distinctly, before the create call below:

```bash
gh issue view <original-ticket> --repo <owner>/<repo> --json milestone,labels > ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-meta.json || rm -f ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-meta.json
```

Ticketless mode skips this fetch entirely — same as it already omits `Related to #<original-ticket>` below.

### Generation limit — no followup from a followup

In ticket mode, reuse the `followup-meta.json` fetched just above (zero new `gh` calls) to decide whether this run may mint a followup at all: read `.labels[].name` and, if it includes `Followup`, then the ticket being implemented in this run is itself a Followup ticket — set `SKIP_FOLLOWUP_CREATE=true` and skip the dedup search, the create block, and the original-ticket comment below entirely, so a Followup ticket's own PR never spawns another Followup; the surviving `## Notes` items stay in this PR's body (already written above) and are simply not re-tracked, and the final session summary notes that followup creation was skipped because this run's own ticket is a Followup.

If the meta fetch failed in ticket mode (no `followup-meta.json` present even though an original ticket exists), also set `SKIP_FOLLOWUP_CREATE=true` and skip — not as a graceful degrade: create no new followup ticket, fail-closed, because an unreadable label list cannot rule out a Followup origin, and re-tracking a followup's deferrals into a fresh followup is exactly the generation chain this limit exists to stop (unlike the cosmetic milestone/label inheritance degrade above, whose worst case is only a missing inherited label). Note in the final session summary that followup creation was skipped fail-closed because the origin ticket's labels could not be fetched (auth/network), so the surviving items and the reason stay visible to the user. Ticketless mode is unchanged: it has no original ticket and no lineage to extend, so the generation limit does not apply and creation proceeds as before.

### Dedup before create — append to an existing open Followup

Run this step (and the create block below, and the comment) only when `SKIP_FOLLOWUP_CREATE` is unset. In ticket mode, before minting anything, search the open Followup backlog for a ticket already tracking this run's original ticket and append to it instead of opening a duplicate:

```bash
gh issue list --repo <owner>/<repo> --label "Followup" --state open --json number,body --limit 200 > ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-search.json
```

`--limit 200` is required: `gh issue list` otherwise caps at its default page size (~30), which would silently miss older open Followups and defeat the dedup once the backlog grows past a page.

Set `MATCH_N` to the lowest-numbered open issue whose `body` contains the back-link `Related to #<original-ticket>` **as a complete line** — matched as an exact whole line via `grep -qxF 'Related to #<original-ticket>'` against the fetched body (the `-x` is load-bearing: plain `grep -qF` is a within-line *substring* match, so ticket #7 would still match a body that only contains `Related to #70`), so a numeric-prefix collision can never fire. `<original-ticket>` is this run's own ticket ID, and the back-link is the exact line this section writes below. This must match on the literal whole back-link line only (`grep -qxF` — never a fuzzy compare); never match on title similarity or any fuzzy heuristic — the back-link is the sole join key, so an unrelated ticket that merely shares words in its title is never treated as a duplicate. If no open issue contains that exact line (or in ticketless mode, which has no back-link to search on), there is no `MATCH_N`: fall through to the create block below unchanged.

If `MATCH_N` is set, append this run's deferred items to it instead of creating a new ticket. Re-read the matched issue's current body, then form the new checklist lines in the same one-line-context + `<file/area>` format as the create body below. Apply a resume-safe idempotency guard against a re-run double-appending: `grep -qF` each candidate line against the existing body and drop any already present; if nothing new remains, skip the edit entirely rather than pushing an empty change. Otherwise write the full updated body — the existing body with the surviving new lines appended under its existing `## Deferred Items` checklist — to `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-body.md`, re-applying no label or milestone (the matched ticket already carries them):

```bash
gh issue view "$MATCH_N" --repo <owner>/<repo> --json body -q .body > ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-existing-body.md
gh issue edit "$MATCH_N" --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-body.md
```

If the `gh issue edit` append fails, do **not** post the original-ticket comment referencing `$MATCH_N` and do **not** treat the items as tracked — report the exact error with the deferred-item list in the session summary, exactly as a failed create is handled below, so the items are not silently lost. On a successful append, set `<n>` = `$MATCH_N` for the original-ticket comment below and skip the create block entirely — the comment then fires so the original ticket links to the followup that now carries its items.

### Create the followup ticket

Reach this block only when `SKIP_FOLLOWUP_CREATE` is unset **and** no `MATCH_N` was found (or in ticketless mode). Use the `Write` tool to create the raw title and body as plain text — `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-title.txt` and `${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-body.md` — never a hand-escaped JSON literal; the title is free text and must never be interpolated directly into the command line (a title containing `$(…)`, backticks, or quotes would be shell-interpreted). Build the payload per the `shell-rules` skill's canonical `jq -n --rawfile` snippet: labels become a JSON `labels` array and the milestone becomes a numeric `milestone` field in the same payload, sourced from `.milestone.number` (the REST endpoint requires the milestone's number, not its title). Carry over every original label except the 10 lifecycle/transient and refinement-granted markers — `"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"` — and `Followup` itself (which is always applied on top regardless of what's carried over); a followup is an untriaged capture-queue item (`docs/followup-triage.md`) that leaves the queue only via triage or promotion through `/cenci:refine`, so it must not arrive pre-carrying refinement-granted markers — least of all a hands-off-merge grant (#848); the `milestone` key is included only when the original ticket actually has one, via an explicit jq emptiness check that omits the key entirely rather than a bare `//` fallback that would emit `null` (see `docs/shell-scripting-gotchas.md`).

Two documented `jq` forms replace the old `if [[ -f … ]]` shell branch:

**With-meta** (the fetch above succeeded):

```bash
jq -n --rawfile title "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-title.txt" --rawfile body "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-body.md" --slurpfile meta "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-meta.json" '{title: ($title | rtrimstr("\n")), body: $body, labels: (["Followup"] + [$meta[0].labels[].name | select(. as $n | (["Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check","Followup"] | index($n)) | not)])} + (if ($meta[0].milestone.number // "") != "" then {milestone: $meta[0].milestone.number} else {} end)' > "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-payload.json"
```

**No-meta** (ticketless mode only — no `--slurpfile`, `labels` is `["Followup"]` only):

```bash
jq -n --rawfile title "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-title.txt" --rawfile body "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-body.md" '{title: ($title | rtrimstr("\n")), body: $body, labels: ["Followup"]}' > "${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-payload.json"
```

Then, whichever form ran:

```bash
gh api repos/<owner>/<repo>/issues -X POST --input ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-payload.json --jq .number
```

The `--jq .number` output *is* the new ticket's issue number `<n>` — this confirms the API accepted valid JSON, but not that the title text itself is correct (a JSON-escaping mistake can mangle a title while still parsing). **Verify the title persisted correctly** by re-fetching the new issue and comparing against the intended title:

```bash
gh issue view <n> --repo <owner>/<repo> --json title --jq '.title'
```

If the create fails, or `--jq .number` returns empty or non-numeric output, or the command exits non-zero, or the re-fetched title does not match, follow the same report/retry-once/STOP protocol as the rest of this phase: report the error (or mismatch), retry once (fresh `Write` of the raw files, fresh `jq` build, fresh `gh api` call), then re-verify; if it still fails, do not fabricate `<n>` and do not post any text referencing it — skip the comment below and report the exact error (with the deferred-item list) in the final session summary so the items aren't silently lost. This also covers the raw title/body `Write` calls and the `jq` invocation: if either `Write` call fails, or `jq` exits non-zero, or the payload file is missing/empty/stale when `gh api --input` runs, retry the failed step once before invoking `gh api` — do not mistake a local Write or jq failure for an API-side rejection.

Ticketless mode falls through to the no-meta form unchanged — the issue is created with `labels: ["Followup"]` only, no `milestone` key — since ticketless has no original ticket, so there is nothing to inherit and no lineage to check. Ticket mode reaches this block only after a *successful* meta fetch (a failed fetch was already handled fail-closed by the generation limit above, which skipped creation), so inheritance is a visibility enhancement, not a correctness gate; if that successful fetch nonetheless returned an empty milestone and no carry-over labels, note in the final session summary that milestone/label inheritance was skipped so the gap is visible.

Body content (checklist of items, each with a one-line context and file/area reference):

```markdown
## Deferred Items
- [ ] <one-line context> — `<file/area>`

Related to #<original-ticket>

PR: <PR URL>
```

Ticket mode: include the `Related to #<original-ticket>` line — `<original-ticket>` is this run's own ticket ID (for a child ticket, the child; the parent is already linked via the commit's `Fixes #<parentId>` on last-child PRs). Ticketless mode: omit it, keep the PR link. The followup ticket does **not** receive the `Refined` label — it enters the backlog unrefined; a human triages it and runs `/cenci:refine` when it's worth doing.

Assume the issue is world-readable: never transcribe secret values, credentials, or exploitable vulnerability detail into the body. Reference deferred security findings abstractly — one neutral line plus the file/area, not the finding's specifics.

If the create fails, or `--jq .number` returns empty, non-numeric output, or a non-zero exit, or the re-fetched title does not match, do **not** fabricate `<n>` and do not post any text referencing it — skip the comment below and report the exact error (with the deferred-item list) in the final session summary so the items aren't silently lost.

Ticket mode only, and only when `SKIP_FOLLOWUP_CREATE` is unset: after the followup issue is created successfully — or after this run appended to an existing `MATCH_N` above — comment on the original ticket (this run's ticket ID) with the followup ticket number `<n>` (the `--jq .number` value from a fresh create, or `$MATCH_N` for an append — never a value parsed from a command's output URL):

```bash
gh issue comment <original-number> --repo <owner>/<repo> --body "Followups tracked in #<n>"
```

Ticketless mode: skip this comment (there is no original ticket to comment on).

## Cleanup

After successful PR creation, archive the consumed plan file instead of deleting it — move it into `.plans/done/` so it survives as a record of what was implemented. `.plans/` lives only in the main checkout (repo root), not in the worktree — it is written during Phase 1, before the worktree even exists, and is never copied into the worktree — so this step must target `<repo-root>` (the main checkout containing `.worktrees/` — resolve via `git -C <worktree-path> rev-parse --path-format=absolute --git-common-dir` if needed, not the worktree itself), not `<worktree-path>`. Guard with a file-existence check: plan-file/ticketless runs where no `.plans/<filename>` exists for this run make this a no-op.

A single `&&`-chained guard cannot distinguish "no-op" from "guard true but mkdir/mv genuinely failed" — both produce the same non-zero exit. Use an if/else that emits a distinguishable marker for each of the three real outcomes instead:

```bash
if [ -f "<repo-root>/.plans/<filename>" ]; then
  mkdir -p "<repo-root>/.plans/done" && mv -n "<repo-root>/.plans/<filename>" "<repo-root>/.plans/done/" \
    && echo "ARCHIVE_OK" || echo "ARCHIVE_FAILED"
else
  echo "ARCHIVE_SKIPPED (no plan file for this run)"
fi
```

This is a plain `mv`, not a git operation — `.plans/` is untracked/gitignored. If the pipeline fails before PR creation, preserve the plan file in place (do not archive it) for retry.

`mv -n` (no-clobber) intentionally leaves a pre-existing `.plans/done/<filename>` in place rather than overwriting it — if this ticket is re-implemented and produces the same `<id>-<slug>.md` name, the earlier archived record is preserved rather than destroyed.

Key the final session summary off the marker, not the command's exit code: report `ARCHIVE_FAILED` (a real failure — permission denied, disk full, cross-device error) in the summary; `ARCHIVE_SKIPPED` and `ARCHIVE_OK` are both expected outcomes and need no special reporting (`ARCHIVE_OK` prints even when `mv -n` silently preserved a pre-existing same-named archive instead of moving into it — `mv -n` exits 0 on that no-clobber skip, same as a normal move).

Finally, delete this run's scoped shared temp files — they were only ever intermediate state for this ticket's pipeline run, and the PR now carries everything they contributed. Phase 6 + 7's four review artifacts (diff patch, changed-file list, stat, review-path) live together in `$RUN_DIR`, so remove the whole directory in one step, guarded on `RUN_DIR` actually being known:

```bash
[[ -n "${RUN_DIR:-}" && -d "$RUN_DIR" ]] && rm -rf "$RUN_DIR"
```

If `$RUN_DIR` is unknown (lost to compaction — see the `## Review`/`## Checklist` fallback above), skip this step; the directory becomes a small, acceptable ephemeral `/tmp` leak rather than a risk of deleting the wrong path.

The remaining per-ticket temp files stay ticket-slug-scoped and out of this ticket's scope:

```bash
rm -f \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-pr-body.md \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-title.txt \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-body.md \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-meta.json \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-payload.json \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-search.json \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-followup-existing-body.md \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-parent-body.md \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-parent-gaps.md \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-explore-1.md \
  ${TMPDIR:-/tmp}/cenci/cenci-<ticket-id-or-slug>-explore-2.md \
  ${TMPDIR:-/tmp}/cenci/cenci-context-<ticket-id-or-slug>.md \
  ${TMPDIR:-/tmp}/cenci/cenci-escalated-<ticket-id-or-slug>.marker
```

This deliberately excludes two other scoped temp locations: the screenshots dir (`${TMPDIR:-/tmp}/cenci/cenci-screenshots/<ticket-id-or-slug>/`) is a documented fallback location kept for gist-upload failures (see Screenshots above), and the gist clone temp dir (`${TMPDIR:-/tmp}/cenci/cenci-gist-<gist-id>/`) is already uniquely scoped by gist ID — neither needs this pass to stay collision-safe.

Like the plan-file archiving above, this cleanup only runs on the success path (PR created). If the pipeline fails before PR creation, these files are preserved for retry/debugging, same as the plan file.

## Babysit

This is the **final** Phase-9 action — do it only after `## Cleanup` above has settled the session's own completion (the plan file was archived). Ordering matters: this step arms an *unattended* `cenci babysit` supervisor that keeps running after this session exits, so the session's own cleanup must be finished before an external watcher is live.

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

3. **Non-fatal & idempotent.** If the launch prints `supervisor already running for PR #<pr-number>`, that is **expected success**, not an error — an earlier attempt already armed the watcher, and `cenci babysit` refuses to start a second supervisor for the same PR. Treat it exactly like the "PR already exists" / "label already exists" cases earlier in this phase: proceed to the report below. Any *other* launch failure (auth, network, missing binary) is reported to the user but does **not** fail the phase — the PR already exists and is the pipeline's real deliverable; a failed watcher launch just means the user should arm it manually.

Finally, report the terminal state as the PR being open and watched, not as done/merged:

> PR #<pr-number> open and being watched by babysit → <pr-url> (stop with `cenci babysit stop <pr-number>`)
