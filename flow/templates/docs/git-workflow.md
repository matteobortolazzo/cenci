# Git Workflow

## Worktrees
All feature work happens in worktrees under `.worktrees/`.
Main worktree stays on `main` — read-only for implementation.

```bash
# Create
git worktree add .worktrees/<id>-<desc> -b feature/<id>-<desc>
```

## Branch Naming
Use the pattern from `.cenci/config.json`:
- GitHub: `feature/<id>-<short-description>`

## Commit Format
```
<type>(<scope>): <description>

<body>

<ticket-ref>
```

Types: feat, fix, refactor, test, docs, chore
Ticket ref: `#123` (GitHub) — per config.json

## PR Workflow
1 ticket = 1 PR targeting `main`. A PR is measured against its ticket's S/M/L size through the `Size:` line in the PR body; a PR above its ticket's size is flagged and must not be merged on green checks alone.
Multiple commits within a PR are fine — use them to organize logical steps.
