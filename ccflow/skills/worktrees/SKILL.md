---
name: worktrees
description: Git worktree patterns for isolated parallel development. Use when creating feature branches, managing git worktrees, isolating feature work, parallel development, setting up a worktree, listing worktrees, worktree naming conventions, or cleaning up worktrees.
user-invocable: false
---

## Structure
```
project-root/          # Main worktree — stays on main, read-only for implementation
├── .worktrees/        # All feature worktrees (gitignored)
│   ├── 12345-feature-a/
│   └── 12346-feature-b/
```

## Rules
- **Never modify code in main worktree** — use it for reading/searching/comparing
- **One worktree per feature** (enables parallel Claude Code instances)
- **Naming**: `.worktrees/<ticket-id>-<short-description>`

## Commands
```bash
# Create worktree for a feature — use git -C from the repo root, no cd compound
git -C <repo-root> worktree add .worktrees/<id>-<desc> -b feature/<id>-<desc> main

# List worktrees
git -C <repo-root> worktree list
```

To inspect the new worktree (e.g. check for `node_modules`), use separate bare `ls`/`git -C` calls — never `cd <repo>; … 2>/dev/null` in one line. A single Bash call combining a `cd` with a redirection or write hits a built-in guard that no setting can disable (see `shell-rules` → the `cd`+redirection guard).

## .gitignore
Ensure `.worktrees/` is in `.gitignore`.
