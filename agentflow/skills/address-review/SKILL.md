---
name: address-review
description: "Claude Code-only: address PR review comments by fetching, evaluating, fixing, replying, pushing, and re-requesting review."
compatibility: Requires Claude Code tools, interactive gates, and agentflow pipeline configuration.
argument-hint: <pr-number> [additional context]
disable-model-invocation: true
user-invocable: true
allowed-tools: Read, Write, Edit, Bash, Glob, Grep, Task, AskUserQuestion
---

Read the `subagent-safety` reference skill before delegating work to subagents.

## Context

**Config check**: Before anything else, verify `.claude/config.json` exists by reading it. If the file does not exist, **stop immediately** and tell the user:
"agentflow is not configured for this project. Run `/agentflow:configure` first to set up."

Read `.claude/config.json`.

**Shell rules**: Read the `shell-rules` skill before running any `gh` commands (covers heredoc temp-file pattern).

**Parse `$ARGUMENTS`:**
The first token is the PR number. Everything after it is optional **user context** (additional instructions or focus areas).

Split `$ARGUMENTS` into:
- **PR number**: the first whitespace-delimited token, with any leading `#` prefix stripped.
  For example: `#42 focus on the API comments` → number `42`, `7` → number `7`.
- **User context**: everything after the first token (may be empty).
  For example: `42 only address the test coverage comments` → context is `only address the test coverage comments`.

Read any relevant `docs/<topic>.md` files for the work area before addressing comments. If a legacy `.claude/rules/lessons-learned.md` exists in the project, read it as fallback.

## Pipeline

This pipeline has 6 phases. Execute them in order. Between major phases, report
progress to the user.

### Phase 1: Fetch PR & Comments
Fetch the PR metadata and all review comments.

<details>
<summary>Phase details</summary>

**Prerequisites**: Config loaded, PR number parsed.

## Step 1A: Fetch PR Metadata

Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`), then run:
```bash
gh pr view <number> --repo <owner>/<repo> --json number,title,body,headRefName,state,reviewDecision,reviews,reviewRequests
```

## Step 1B: Pre-flight Check

Verify the PR is open:
- If the PR is **merged** → warn: "This PR is already merged. Nothing to address."  Stop.
- If the PR is **closed** → warn: "This PR is closed. Do you want to proceed anyway?" Use `AskUserQuestion`. If no → stop.

## Step 1C: Fetch Review Comments

Run both in parallel:
```bash
gh api repos/<owner>/<repo>/pulls/<number>/reviews
```
```bash
gh api repos/<owner>/<repo>/pulls/<number>/comments
```

## Step 1D: Filter to Actionable Comments

Read the `pr-comment-filter` reference skill and apply its include/exclude filter to the fetched comments. That skill is the single source of truth for this filter — `babysit` applies the same one, and its watermark only works if the two match.

If **no actionable comments** remain after filtering → report "No actionable review comments found on this PR." and stop.

</details>

### Phase 2: Navigate to Working Directory
Find or check out the PR branch.

<details>
<summary>Phase details</summary>

**Prerequisites**: PR metadata fetched, PR is open, actionable comments exist.

## Step 2A: Locate Working Directory

Check if a worktree exists for this PR's branch:
```bash
git worktree list --porcelain
```

Scan the output for a worktree whose branch matches the PR's `headRefName`. Also check `.worktrees/` directory.

## Step 2B: Enter Working Directory

**If worktree exists**: Use it as the working directory for all subsequent phases.

**If no worktree exists**: Check out the PR branch:
```bash
gh pr checkout <number>
```

## Step 2C: Ensure Branch is Up to Date

```bash
git pull --rebase origin <headRefName>
```

If the pull fails (e.g., conflicts), warn the user and ask how to proceed.

</details>

### Phase 3: Present & Evaluate Comments
Group, evaluate, and get user approval on how to handle each comment.

<details>
<summary>Phase details</summary>

**Prerequisites**: Working in the PR branch, actionable comments filtered.

## Step 3A: Group Comments

Group comments by reviewer and thread. For each thread, capture:
- Reviewer name
- File path and line range (if inline)
- Comment body
- Thread context (previous replies in the conversation)
- Comment ID (needed for replies in Phase 5)

## Step 3B: Present Summary

Present a high-level summary to the user:
- Total actionable comments
- Count per reviewer
- Breakdown: how many are inline code comments vs. general PR comments

**If user context was provided** in `$ARGUMENTS`, mention it and explain how it steers your evaluation.

## Step 3C: Evaluate Each Comment

For each comment/thread, evaluate using these principles:

1. **Verify before implementing** — check the reviewer's claim against the actual codebase. Is the issue real?
2. **Technically sound?** — does the suggestion make sense for this codebase's patterns and constraints?
3. **YAGNI check** — is the suggestion adding unnecessary complexity, over-engineering, or premature abstraction?
4. **Conflict check** — does it conflict with prior architectural decisions documented in `CLAUDE.md` or `docs/<topic>.md`?
5. **Clarity check** — is the feedback clear enough to implement, or is it ambiguous?

## Step 3D: Recommend Actions

For each comment, recommend one of:

| Action | When to use |
|--------|-------------|
| **Fix** | The feedback is valid and the change should be made |
| **Push back** | The suggestion is incorrect, conflicts with architecture, or is YAGNI |
| **Clarify** | The feedback is ambiguous — need more info from the reviewer before acting |
| **Acknowledge** | Valid point but out of scope for this PR — defer to future work |

## Step 3E: Implementation Order

Sort the "Fix" items by priority:
1. **Blocking issues first** — bugs, broken behavior, security concerns
2. **Simple fixes second** — naming, formatting, small logic changes
3. **Complex fixes last** — refactoring, architectural changes

## Step 3F: User Approval

Present the full evaluation to the user: each comment with your recommended action and reasoning.

Use `AskUserQuestion` to confirm the plan. Options:
- **Approve** — proceed with the recommended actions
- **Modify** — user wants to change some actions

If "Modify": ask which comments to change and what action to take instead, then re-present.

**Only proceed to Phase 4 after the user approves.**

</details>

### Phase 4: Implement Fixes
Make code changes for all comments marked "Fix".

<details>
<summary>Phase details</summary>

**Prerequisites**: User approved the action plan, working in the PR branch.

## Process

For each comment marked **Fix**, in the priority order from Phase 3:

1. Read the relevant file(s) and understand the context around the comment
2. Make the code change
3. Run relevant tests (unit tests for the affected file/module)
4. If tests fail:
   - Analyze the failure
   - Fix the root cause
   - Re-run tests
   - If still failing after 3 attempts, stop and ask the user
5. Move to the next fix

After all individual fixes are applied, run the full build and test suite:
```bash
<build command from config or CLAUDE.md>
<test command from config or CLAUDE.md>
```

## Error Recovery

If the full test suite fails after all fixes:
1. Identify which fix broke the tests
2. Attempt to fix the issue (up to 3 retries)
3. If still failing, report to the user with:
   - The exact error output
   - Which review comment's fix caused the failure
   - Your best hypothesis for the root cause

</details>

### Phase 5: Reply to Comments
Post replies on each review comment thread.

<details>
<summary>Phase details</summary>

**Prerequisites**: All fixes implemented and tests passing (or user has approved proceeding despite failures).

## Reply Templates

For each comment, post a reply based on the action taken:

| Action | Reply format |
|--------|-------------|
| **Fixed** | "Fixed — [brief description of what changed]" |
| **Pushed back** | "[Technical reasoning why the suggestion isn't appropriate]" |
| **Clarify** | "[Specific question for the reviewer]" |
| **Acknowledge** | "Noted — deferring to [ticket/future work] because [reason]" |

**Tone rules** (from receiving-code-review principles):
- No performative gratitude — skip "Great point!", "Thanks for catching this!", etc.
- Technical acknowledgment only — state what was done or why not
- Be direct and concise

## Posting Replies

For each inline review comment:
```bash
printf '%s' '<reply text>' > /tmp/claude/pr-reply-<comment-id>.md
REPLY=$(cat /tmp/claude/pr-reply-<comment-id>.md)
gh api repos/<owner>/<repo>/pulls/<number>/comments/<comment-id>/replies -f body="$REPLY"
```

For general PR review comments, post as a PR comment:
```bash
printf '%s' '<reply text>' > /tmp/claude/pr-comment.md
COMMENT=$(cat /tmp/claude/pr-comment.md)
gh pr comment <number> --repo <owner>/<repo> --body "$COMMENT"
```

## Resolve Threads

Threads are resolved by the reviewer — do not attempt to resolve them.

</details>

### Phase 6: Push & Re-request Review
Commit changes, push to the PR branch, and re-request review.

<details>
<summary>Phase details</summary>

**Prerequisites**: All fixes applied, tests passing, replies posted.

## Step 6A: Commit

Stage and commit all changes:
```bash
git add -A
git commit -m "fix(review): address PR feedback

- <summary of changes made>"
```

If no files were changed (all comments were pushed back, clarified, or acknowledged), skip the commit and push steps.

## Step 6B: Push

```bash
git push origin <headRefName>
```

If the push **fails** (e.g., sandbox network restriction, SSH remote):
1. Display the exact push command to the user
2. Explain that the sandbox may be blocking the push
3. Ask the user to run the push command manually outside Claude Code
4. Wait for user confirmation before proceeding

## Step 6C: Re-request Review

Re-request review from the reviewers who left comments:
```bash
gh pr edit <number> --repo <owner>/<repo> --add-reviewer <reviewer-login>
```
Run once per reviewer who left actionable comments.

## Step 6D: Report Summary

Present a final summary to the user:

```
## Review Addressed

PR #<number>: <title>

- **Fixed**: N comments
- **Pushed back**: N comments
- **Clarified**: N comments
- **Acknowledged**: N comments

Changes committed and pushed. Review re-requested from: <reviewer list>
```

</details>

## After Addressing Review

**STOP HERE.** Your job is done. Do not:
- Offer to merge the PR
- Suggest additional changes beyond what reviewers requested
- Enter plan mode or propose further implementation
- Run additional review cycles unless the user explicitly asks
