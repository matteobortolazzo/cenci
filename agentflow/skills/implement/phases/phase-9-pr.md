# Phase 9: Create PR

Read this file only when Phase 9 starts.

This phase is pre-approved — commit, push, and create the PR without asking for confirmation. The only exceptions are the error cases defined below (rebase conflicts, test failures after rebase, push auth/network failures).

**Goal Autopilot**: if a goal was armed at Phase 2 (see `SKILL.md` → Goal Autopilot), it must be cleared here. Clear it on success (right after the PR is created, before plan-file cleanup) **and** before any of this phase's error gates hands control back to the user — an un-cleared goal restarts the turn and would loop on an unrecoverable state (a rebase conflict, a failed push). Run `/goal clear` (via the `SlashCommand` tool) at those points; it is a safe no-op if no goal was armed or `/goal` is unavailable.

Prerequisites: all required reviews complete, Must Fix/Critical/High items resolved, build and tests pass.

Read `<worktree-path>/docs/git-workflow.md`; if absent, read legacy `<worktree-path>/.claude/rules/git-workflow.md`.

Source `ticketId`, `slug`, `isChild`, `isLastChild`, and `parentId` from plan front matter when `hasPlanFile` is true.

The worktree must be the CWD first — run a standalone `cd <worktree-path>` before the rebase/commit/push commands below so they resolve against the worktree and stay auto-approved.

## Rebase

Fetch and rebase:

```bash
git fetch origin main
git rebase origin/main
```

If rebase succeeds, rerun full build and tests. If tests fail, clear the goal (`/goal clear`), then stop and report the rebase-induced failure.

If rebase conflicts, abort, clear the goal (`/goal clear`), report conflicting files, and stop:

```bash
git rebase --abort
```

Tell the user to resolve manually, rerun build/tests, and resume from commit.

## Commit

Stage and commit:

```bash
git add -A
git commit -m "<type>(<scope>): <description>

<body if needed>

<ticket-ref>"
```

Ticket mode:

- Normal child/non-child: `Fixes #<childId or ticketId>`.
- Last child: include both `Fixes #<childId>` and `Fixes #<parentId>`.

Ticketless mode: no ticket reference.

## Push

Push the branch:

- Ticket mode: `git push -u origin feature/<ticket-id>-<description>`
- Ticketless mode: `git push -u origin feature/<auto-slug>`

If push fails due to sandbox/network/auth, clear the goal (`/goal clear`), show the exact command, and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.

## Screenshots (UI Work)

Skip this section unless `isUiTicket` is true.

Screenshots are temporary review aids — never commit them to the repo. Host them in a **secret GitHub gist**: it lives under the user's account, is unlisted, and is disposable (`gh gist delete <gist-id>` after merge).

1. **Collect**: use the images Phase 4 persisted to `/tmp/claude/agentflow-screenshots/<ticket-id-or-slug>/`. If the directory is missing or empty and `playwright-cli` is available, capture the affected screens now against the dev build. If capture is not possible, skip the upload and use the fallback body in step 5.
2. **Privacy check**: secret gists are unlisted but readable by anyone with the URL. Screenshots must show only local/dev data — no real user data, tokens, or internal URLs. Crop or re-capture rather than upload.
3. **Create the gist** (gists require a text file at creation; images are pushed via git afterwards):

   ```bash
   printf 'Screenshots for %s — temporary, delete after merge.\n' "<branch>" > /tmp/claude/agentflow-screenshots/README.md
   gh gist create --desc "agentflow screenshots: <owner/repo> <branch>" /tmp/claude/agentflow-screenshots/README.md
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

Create the PR with `gh pr create`. Write body content to `/tmp/claude/pr-body.md` first and read it back; do not use heredocs or a large inline body string.

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

`## Review` reports which Phase 6 + 7 path ran, sourced from `/tmp/claude/agentflow-review-path.txt` (written during Phase 6 + 7's Review Path Classification):

- `full` → "Review: full trio"
- `lite-docs` → "Review: lite (docs-only — no reviewers)"
- `lite-small` → "Review: lite (code-reviewer only)"

If the temp file is absent (e.g. after a context compaction or a goal-autopilot resume that skipped re-reading it), default to "Review: full trio" — never over-claim a lite path when the actual path isn't known.

The `## Checklist` security line is derived from the same `/tmp/claude/agentflow-review-path.txt` file, in the same read:

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

## Cleanup

After successful PR creation, first clear the Goal Autopilot condition — the PR now exists, so the goal is met:

```
/goal clear
```

Run it via the `SlashCommand` tool; it is a no-op if no goal was armed. Clearing before the plan-file deletion below keeps the two "work is done" signals in sync: the goal's condition references `.plans/<filename>`, which is removed next.

Then delete the consumed plan file. If `.plans/` is empty, remove it. If the pipeline fails before PR creation, preserve the plan file for retry (and, as above, clear the goal at whichever error gate stopped the run).
