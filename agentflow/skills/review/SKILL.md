---
name: review
description: "Claude Code-only: review code with specialized security, quality, and silent-failure subagents."
compatibility: Requires Claude Code subagents and interactive gates.
argument-hint: [<pr-number> | <file-paths>]
user-invocable: true
disable-model-invocation: true
allowed-tools: Read, Bash, Glob, Grep, Task, AskUserQuestion
---

## Context

Read `.claude/config.json`.
Read relevant `docs/<topic>.md` files for the area under review. If a legacy `.claude/rules/lessons-learned.md` exists in the project, read it as fallback.

**Shell rules**: Read the `shell-rules` skill before running any `gh` commands.
**Subagent safety**: Read the `subagent-safety` skill before delegating work to subagents.

## Parse `$ARGUMENTS`

Determine the review mode from `$ARGUMENTS`:

- **If empty** → **diff mode**: Review the current uncommitted diff (`git diff` + `git diff --cached`)
- **If first token is a number** (matches `^\d+$` or `^#\d+$`) → **PR mode**: Review a specific PR
- **Otherwise** → **file mode**: Review the specified file paths or glob patterns

## Phase 1: Gather Context

### Diff Mode (no arguments)

```bash
git diff
git diff --cached
```

If both are empty, fall back to the diff against main:
```bash
git diff main...HEAD
```

If still empty, report "No changes to review" and stop.

### PR Mode (PR number provided)

Extract owner/repo from `git remote get-url origin`, then:
```bash
gh pr diff <number> --repo <owner>/<repo>
```

Also fetch the PR metadata for context:
```bash
gh pr view <number> --repo <owner>/<repo> --json title,body,headRefName
```

### File Mode (file paths provided)

Expand any glob patterns with the Glob tool and collect the **list of file paths**. Do **not** read the file contents in the main agent — all three reviewers have `Read` and read the files they need themselves. Pasting contents here would duplicate every file into the main context plus each of the three Task prompts.

## Phase 2: Parallel Review

**Prepare shared context by path, not by paste.** For diff/PR mode: if the diff is small (roughly under 200 lines), it may be passed inline; otherwise write it once to `/tmp/claude/agentflow-review-diff.patch` (plus the changed-file list from `git diff --name-only`) and pass reviewers the path — the same `diffContextMode: "file"` discipline the implement pipeline uses. For file mode: pass the file path list only.

Launch **all three reviewers as parallel Task tool calls in a SINGLE message**:

1. **security-reviewer** agent — pass the diff (inline or patch path) or file path list
2. **code-reviewer** agent — pass the diff (inline or patch path) or file path list, plus any PR context if available
3. **silent-failure-hunter** agent — pass the diff (inline or patch path) or file path list

Tell each reviewer to read only the hunks/files relevant to its focus.

Wait for all three to complete.

## Phase 3: Consolidate Results

Merge findings from all three reviewers into a unified report.

### Categorization

Group findings into three tiers:

**Critical** (must address):
- Security: CRITICAL or HIGH severity
- Code: Must Fix (confidence >= 90)
- Silent Failures: Critical (error swallowed in sensitive paths)

**Important** (should address):
- Security: MEDIUM severity
- Code: Should Fix (confidence 75–89)
- Silent Failures: Warning

**Suggestions** (consider):
- Security: LOW severity
- Code: Nitpicks (confidence 50–74)
- Silent Failures: Info

### Deduplication

If multiple reviewers flag the same location:
- Keep the finding with the most detail
- Note which reviewers flagged it (adds confidence)
- Don't report the same issue twice

### Report Format

```markdown
## Code Review Report

**Scope**: <diff/PR #N/files reviewed>
**Files reviewed**: <count>

---

### Critical (<count>)

#### C1. <title>
- **Reviewer**: <security-reviewer | code-reviewer | silent-failure-hunter>
- **Location**: `path/to/file:line`
- **Issue**: <description>
- **Fix**: <suggestion>

---

### Important (<count>)

#### I1. <title>
- **Reviewer**: <reviewer>
- **Location**: `path/to/file:line`
- **Issue**: <description>
- **Fix**: <suggestion>

---

### Suggestions (<count>)

#### S1. <title>
- **Reviewer**: <reviewer>
- **Location**: `path/to/file:line`
- **Issue**: <description>

---

### Passed Checks
- [x] <checks that passed from all reviewers>

### Positive Notes
- <what was done well>

### Verdict
<CLEAN | HAS_ISSUES | NEEDS_WORK>
- CLEAN: No critical or important findings
- HAS_ISSUES: Has important findings but no critical ones
- NEEDS_WORK: Has critical findings that must be addressed
```

## Phase 4: Optional PR Comment

**Only in PR mode.** After presenting the report, ask using `AskUserQuestion`:

> "Would you like me to post this review as a PR comment?"

If yes:
```bash
printf '%s' '<review report>' > /tmp/claude/review-comment.md
BODY=$(cat /tmp/claude/review-comment.md)
gh pr comment <number> --repo <owner>/<repo> --body "$BODY"
```

If no → stop after presenting the report.

## After Review

**STOP HERE.** Do not:
- Offer to fix the findings
- Enter plan mode or propose implementation
- Suggest running `/implement`

The user will decide what to do with the findings.
