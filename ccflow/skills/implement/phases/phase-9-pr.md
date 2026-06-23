# Phase 9: Create PR

Read this file only when Phase 9 starts.

This phase is pre-approved — commit, push, and create the PR without asking for confirmation. The only exceptions are the error cases defined below (rebase conflicts, test failures after rebase, push auth/network failures).

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

If rebase succeeds, rerun full build and tests. If tests fail, stop and report the rebase-induced failure.

If rebase conflicts, abort, report conflicting files, and stop:

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

If push fails due to sandbox/network/auth, show the exact command and wait for user confirmation after they push manually.

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

## Checklist
- [x] Tests pass
- [x] Security review done
- [ ] Documentation updated

## Notes
<Medium/Low security findings, deferred Should Fix items, or "None">
```

For child tickets that are not last child, use `Related to #<parentId>` for the parent so it is not auto-closed. For ticketless mode, omit `## Ticket`.

## Labels

Ticket mode: after PR creation, replace "Working" with "Implemented":

```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Implemented" --remove-label "Working"
```

If `isLastChild`, also add "Implemented" to the parent.

## Cleanup

After successful PR creation, delete the consumed plan file. If `.plans/` is empty, remove it. If the pipeline fails before PR creation, preserve the plan file for retry.
