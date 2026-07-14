# Phase 9: Create PR

Read this file only when Phase 9 starts.

This phase is pre-approved — commit, push, and create the PR without asking for confirmation. The only exceptions are the error cases defined below (rebase conflicts, build/test/lint failures after rebase, push auth/network failures).

**Goal Autopilot**: if a goal was armed at Phase 2 (see `SKILL.md` → Goal Autopilot), it must be cleared here. Clear it on success (right after the PR is created, before plan-file cleanup) **and** before any of this phase's error gates hands control back to the user — an un-cleared goal restarts the turn and would loop on an unrecoverable state (a rebase conflict, a failed push). Run `/goal clear` (via the `SlashCommand` tool) at those points; it is a safe no-op if no goal was armed or `/goal` is unavailable.

**Atomicity rule — always restart at Rebase.** Any (re)entry into this phase — a fresh run or a Goal Autopilot resume mid-phase — MUST restart at the Rebase step below and run the steps in order: Rebase → build → test → lint → Commit → Push → PR. Never resume directly at Commit, Push, or PR creation, even if a prior turn already reached one of those steps. This guarantees push/PR creation is always immediately preceded, in the same turn, by a passing rebase + build + test + lint on the current tree — main may have moved between turns, and a stale rebase/verify from an earlier turn cannot be trusted. This is a stateless, markdown-level rule (no marker file, no new state): rebase and re-verify are cheap and idempotent, so restarting from the top on every entry is always safe. The Commit step below handles the case where a prior turn already committed (nothing left to stage) so the restart cannot double-commit or error out.

Prerequisites: all required reviews complete, Must Fix/Critical/High items resolved, build, tests, and lint pass.

Read `<worktree-path>/docs/git-workflow.md`; if absent, read legacy `<worktree-path>/.claude/rules/git-workflow.md`.

Source `ticketId`, `slug`, `isChild`, `isLastChild`, and `parentId` from plan front matter when `hasPlanFile` is true.

The worktree must be the CWD first — run a standalone `cd <worktree-path>` before the rebase/commit/push commands below so they resolve against the worktree and stay auto-approved.

## Rebase

Fetch and rebase:

```bash
git fetch origin main
git rebase origin/main
```

If rebase succeeds, rerun full build and tests, then lint (when `lintCommand` is set). An absent `lintCommand` skips the lint step cleanly — no error. If build, tests, or lint fail, clear the goal (`/goal clear`), then stop and report the rebase-induced failure. Lint is an unconditional hard gate here, exactly like build/test: no PR is created if it fails.

If rebase conflicts, abort, clear the goal (`/goal clear`), report conflicting files, and stop:

```bash
git rebase --abort
```

Tell the user to resolve manually. Per the atomicity rule above, the next entry into this phase still restarts at the Rebase step, not at Commit — the fetch+rebase is a no-op once the user has already resolved and completed it locally, and this guarantees a fresh build/test/lint pass on the rebased tree before Commit runs.

## Commit

Stage and commit:

```bash
git add -A
git commit -m "<type>(<scope>): <description>

<body if needed>

<ticket-ref>"
```

If a prior turn already committed this work (e.g. a Goal Autopilot resume re-entering after Commit ran once), `git add -A` stages nothing new and `git commit` reports nothing to commit — that is expected, not an error. Skip the commit in that case and proceed to Push; do not create an empty commit or fail the phase over it.

Ticket mode:

- Normal child/non-child: `Fixes #<childId or ticketId>`.
- Last child: include both `Fixes #<childId>` and `Fixes #<parentId>`.

Ticketless mode: no ticket reference.

## Push

Push the branch:

- Ticket mode: `git push -u origin feature/<ticket-id>-<description>`
- Ticketless mode: `git push -u origin feature/<auto-slug>`

If this branch was already pushed in a prior turn (a Goal Autopilot resume) and the atomicity rule's mandatory Rebase restart above rewrote local commit SHAs, the plain push above is rejected as non-fast-forward — this is expected, not a failure. Retry once with `git push --force-with-lease -u origin <branch>`: `--force-with-lease` still refuses if the remote tip isn't what this rebase started from (i.e. someone else pushed to the branch), which surfaces as a genuine conflict to report rather than silently overwriting work.

If push fails due to sandbox/network/auth, clear the goal (`/goal clear`), show the exact command, and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.

## Screenshots (UI Work)

Skip this section unless `isUiTicket` is true.

Screenshots are temporary review aids — never commit them to the repo. Host them in a **secret GitHub gist**: it lives under the user's account, is unlisted, and is disposable (`gh gist delete <gist-id>` after merge).

1. **Collect**: use the images Phase 4 persisted to `/tmp/claude/agentflow-screenshots/<ticket-id-or-slug>/`. If the directory is missing or empty and `playwright-cli` is available, capture the affected screens now against the dev build. If capture is not possible, skip the upload and use the fallback body in step 5.
2. **Privacy check**: secret gists are unlisted but readable by anyone with the URL. Screenshots must show only local/dev data — no real user data, tokens, or internal URLs. Crop or re-capture rather than upload.
3. **Create the gist** (gists require a text file at creation; images are pushed via git afterwards):

   ```bash
   printf 'Screenshots for %s — temporary, delete after merge.\n' "<branch>" > /tmp/claude/agentflow-screenshots/<ticket-id-or-slug>-README.md
   gh gist create --desc "agentflow screenshots: <owner/repo> <branch>" /tmp/claude/agentflow-screenshots/<ticket-id-or-slug>-README.md
   ```

   The command prints the gist URL; extract `<gist-id>` from it. Do not pass `--public` — the gist must stay secret.
4. **Push the images** through the gist's git remote (no `cd` compounds — see the `shell-rules` skill):

   ```bash
   gh gist clone <gist-id> /tmp/claude/agentflow-gist-<gist-id>
   cp /tmp/claude/agentflow-screenshots/<ticket-id-or-slug>/*.png /tmp/claude/agentflow-gist-<gist-id>/
   git -C /tmp/claude/agentflow-gist-<gist-id> add -A
   git -C /tmp/claude/agentflow-gist-<gist-id> commit -m "PR screenshots"
   git -C /tmp/claude/agentflow-gist-<gist-id> push
   ```
5. **Build embed URLs**: `https://gist.githubusercontent.com/<gh-user>/<gist-id>/raw/<filename>.png`, where `<gh-user>` comes from `gh api user -q .login`. These go into the PR body's `## Screenshots` section (template below). If any gist step fails (auth, network), do not block PR creation — write `## Screenshots` with "Not uploaded (<reason>); local copies at `/tmp/claude/agentflow-screenshots/<ticket-id-or-slug>/`" instead.

## PR

Create the PR with `gh pr create`. Write body content to `/tmp/claude/agentflow-<ticket-id-or-slug>-pr-body.md` first and read it back; do not use heredocs or a large inline body string.

If a prior turn already created the PR (a Goal Autopilot resume re-entering after PR creation ran once but the turn ended before `/goal clear`), `gh pr create` fails with "a pull request for branch ... already exists." That is not a failure — run `gh pr view <branch> --json url -q .url` to recover the existing PR URL and continue to Labels/Cleanup as if creation had just succeeded.

If `gh pr create` fails for any other reason (auth, network, validation), clear the goal (`/goal clear`), show the exact failing command and its error output, and use `AskUserQuestion` ("Created, continue" / "Abort") to let the user resolve the issue and confirm before continuing to Labels/Cleanup, or abort the run — mirroring the Push gate above. On "Created, continue," re-run `gh pr view <branch> --json url -q .url` to obtain the PR URL/number before proceeding — the same recovery call as the "already exists" case above — since Labels/Cleanup and the Followup Ticket step below need it.

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
- [ ] Documentation updated

## Notes
<Medium/Low security findings, deferred Should Fix items, or "None">
```

For child tickets that are not last child, use `Related to #<parentId>` for the parent so it is not auto-closed. For ticketless mode, omit `## Ticket`.

`## Review` reports which Phase 6 + 7 path ran, sourced from `/tmp/claude/agentflow-<ticket-id-or-slug>-review-path.txt` (written during Phase 6 + 7's Review Path Classification):

- `full` → "Review: full trio"
- `lite-docs` → "Review: lite (docs-only — no reviewers)"
- `lite-small` → "Review: lite (code-reviewer only)"

If the temp file is absent (e.g. after a context compaction or a goal-autopilot resume that skipped re-reading it), default to "Review: full trio" — never over-claim a lite path when the actual path isn't known.

The `## Checklist` security line is derived from the same `/tmp/claude/agentflow-<ticket-id-or-slug>-review-path.txt` file, in the same read:

- `full` → `- [x] Security review done`
- `lite-docs` or `lite-small` → `- [ ] Security review skipped (see Review section — <path>)`, where `<path>` is the literal path value (`lite-docs` or `lite-small`)
- File absent → default to the `full` behavior (`- [x] Security review done`), consistent with the "default to full trio" fallback above — never claim a security review was skipped when the actual path isn't known.

`## Screenshots` appears only when `isUiTicket` is true: one `### <name>` + image per captured screen/state, or the fallback note from the Screenshots section above. Omit the section entirely for non-UI work. If the user chose "Proceed without design" at the Design Check, add "Implemented without design spec — extra visual review recommended." to `## Notes`.

## Labels

Ticket mode: after PR creation, replace "Working" with "In Review" (the PR is open but not yet merged). `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure it exists first — each as its own Bash call (`|| true` swallows only the "already exists" error):

```bash
gh label create "In Review" --repo <owner>/<repo> --color "A2EEEF" --description "PR open, under review / CI running" 2>/dev/null || true
```

```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "In Review" --remove-label "Working"
```

If `isLastChild`, also add "In Review" to the parent. The parent's real completion — the transition to "Implemented" — arrives when this last child's PR merges: the last-child commit carries `Fixes #<parentId>` (see Commit above), so the parent appears in the PR's `closingIssuesReferences` and babysit relabels it on merge.

The `Working` → `In Review` → `Implemented` progression finishes on merge: babysit swaps `In Review` for `Implemented` on any issue closed by the merged PR (see the babysit skill's terminal check). PR-open never applies `Implemented`.

## Followup Ticket

The `## Notes` section above (deferred Should Fix items, Medium/Low security findings) is the formal source of deferred items. Combine it with any informal out-of-scope observations recalled from this session (tech debt spotted, refactor ideas, missing tests noticed but out of scope — no new tracking file, just what the session actually surfaced).

If there is **nothing** to capture (no `## Notes` items and no informal observations), create no ticket and skip this section entirely.

If ≥1 deferred item exists, ensure the label exists (its own Bash call — note `2>/dev/null || true` suppresses **every** failure, not just "already exists"; a genuine failure (auth, network, permissions) surfaces on the next command as a "label not found" error from `gh issue create` — treat that as the label-create failure it is):

```bash
gh label create "Followup" --repo <owner>/<repo> --color "C5DEF5" --description "Deferred/out-of-scope item captured from a session — triage before working" 2>/dev/null || true
```

Write the body to `/tmp/claude/agentflow-<ticket-id-or-slug>-followup-body.md` with the file tool — and the title too, to `/tmp/claude/agentflow-<ticket-id-or-slug>-followup-title.txt`: the PR title is free text and must never be interpolated directly into the command line (a title containing `$(…)`, backticks, or quotes would be shell-interpreted). Then create the ticket in one call, reading the title back the same way Posting Replies in `address-review` reads reply text:

```bash
TITLE=$(cat /tmp/claude/agentflow-<ticket-id-or-slug>-followup-title.txt) && gh issue create --repo <owner>/<repo> --title "$TITLE" --label "Followup" --body-file /tmp/claude/agentflow-<ticket-id-or-slug>-followup-body.md
```

Body content (checklist of items, each with a one-line context and file/area reference):

```markdown
## Deferred Items
- [ ] <one-line context> — `<file/area>`

Related to #<original-ticket>

PR: <PR URL>
```

Ticket mode: include the `Related to #<original-ticket>` line — `<original-ticket>` is this run's own ticket ID (for a child ticket, the child; the parent is already linked via the commit's `Fixes #<parentId>` on last-child PRs). Ticketless mode: omit it, keep the PR link. The followup ticket does **not** receive the `Refined` label — it enters the backlog unrefined; a human triages it and runs `/agentflow:refine` when it's worth doing.

Assume the issue is world-readable: never transcribe secret values, credentials, or exploitable vulnerability detail into the body. Reference deferred security findings abstractly — one neutral line plus the file/area, not the finding's specifics.

If `gh issue create` fails, or no issue number can be parsed from its output URL, do **not** fabricate `<n>` and do not post any text referencing it — skip the comment below and report the exact error (with the deferred-item list) in the final session summary so the items aren't silently lost.

Ticket mode only: after the followup issue is created successfully, comment on the original ticket (this run's ticket ID) with the followup ticket number `<n>` parsed from the create command's output URL:

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

Finally, delete this run's scoped shared temp files — they were only ever intermediate state for this ticket's pipeline run, and the PR now carries everything they contributed:

```bash
rm -f \
  /tmp/claude/agentflow-<ticket-id-or-slug>-diff.patch \
  /tmp/claude/agentflow-<ticket-id-or-slug>-files.txt \
  /tmp/claude/agentflow-<ticket-id-or-slug>-stat.txt \
  /tmp/claude/agentflow-<ticket-id-or-slug>-review-path.txt \
  /tmp/claude/agentflow-<ticket-id-or-slug>-pr-body.md \
  /tmp/claude/agentflow-<ticket-id-or-slug>-followup-title.txt \
  /tmp/claude/agentflow-<ticket-id-or-slug>-followup-body.md \
  /tmp/claude/agentflow-<ticket-id-or-slug>-explore-1.md \
  /tmp/claude/agentflow-<ticket-id-or-slug>-explore-2.md \
  /tmp/claude/agentflow-context-<ticket-id-or-slug>.md
```

This deliberately excludes two other scoped temp locations: the screenshots dir (`/tmp/claude/agentflow-screenshots/<ticket-id-or-slug>/`) is a documented fallback location kept for gist-upload failures (see Screenshots above), and the gist clone temp dir (`/tmp/claude/agentflow-gist-<gist-id>/`) is already uniquely scoped by gist ID — neither needs this pass to stay collision-safe.

Like the plan-file deletion above, this cleanup only runs on the success path (PR created). If the pipeline fails before PR creation, these files are preserved for retry/debugging, same as the plan file.
