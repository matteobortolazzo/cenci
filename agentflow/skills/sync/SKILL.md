---
name: sync
description: "Claude Code-only: sync the main branch, rebase active worktrees, and clean up merged branches through the agentflow command workflow."
compatibility: Requires Claude Code skill arguments and model-selection extensions.
argument-hint: [additional context]
user-invocable: true
disable-model-invocation: true
model: haiku
allowed-tools: Bash, Read
---

## Task

Sync the local repository: pull latest main, rebase active worktrees onto updated main, prune stale remote references, and clean up local branches and worktrees left over from merged PRs.

### Parse `$ARGUMENTS`

All of `$ARGUMENTS` is optional **user context** (additional instructions or focus areas).
If empty, proceed normally with full sync.

## Process

If user context was provided, use it to steer the sync (e.g., skip certain steps, focus on specific cleanup).

### Step 1: Check for uncommitted work

```bash
git status --short
```

If the current branch has uncommitted changes, **warn the user** and ask whether to stash, commit, or abort before continuing.

### Step 2: Update main

```bash
# Fetch all remotes and prune deleted remote branches
git fetch --all --prune

# Switch to main (or master — use whichever exists)
git checkout main 2>/dev/null || git checkout master

# Fast-forward to latest
git pull --ff-only
```

If `pull --ff-only` fails (local main has diverged), **stop and warn the user** — do not force-reset.

### Step 3: Rebase active worktrees

For each non-main worktree (skip any marked `[gone]` in `git branch -v`):

1. **Check for detached HEAD** — skip if detached:
   ```bash
   git -C <worktree-path> symbolic-ref --short HEAD
   ```
   If this fails, the worktree is in detached HEAD state — skip it.

2. **Check for uncommitted changes** — skip if dirty:
   ```bash
   git -C <worktree-path> status --porcelain
   ```
   If output is non-empty, skip this worktree (uncommitted changes would conflict with rebase).

3. **Rebase onto main**:
   ```bash
   git -C <worktree-path> rebase main
   ```

4. **If rebase fails** (conflicts): abort and note the failure, then continue to the next worktree:
   ```bash
   git -C <worktree-path> rebase --abort
   ```

Track results for the report: which worktrees were rebased, skipped (dirty/detached), or had conflicts.

### Step 4: Prune stale worktrees

```bash
git worktree prune
```

This removes worktree entries whose directories no longer exist on disk.

### Step 5: Clean up gone branches

List branches whose remote tracking branch has been deleted (marked `[gone]`):

```bash
git branch -v
```

Capture the worktree list once, then check each gone branch against the captured output:

```bash
WORKTREES=$(git worktree list)
```

For each branch marked `[gone]`:
1. Find any associated worktree in the captured output:
   ```bash
   echo "$WORKTREES" | grep "\[$branch\]"
   ```
2. If a worktree exists and is **not** the main worktree, remove it:
   ```bash
   git worktree remove <worktree-path>
   ```
3. Delete the local branch:
   ```bash
   git branch -D "$branch"
   ```

### Step 6: Report

Summarize what was done:
- Current main commit (short hash + subject)
- Rebase results: which worktrees were rebased successfully, which were skipped (dirty/detached), which had conflicts
- Number of branches cleaned up (list them)
- Number of worktrees removed (list paths)
- Any remaining worktrees (`git worktree list`)

If nothing needed cleaning or rebasing, say so.
