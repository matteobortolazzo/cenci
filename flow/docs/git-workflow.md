# Git Workflow

## Worktrees
All feature work happens in worktrees under `.worktrees/`.
Main worktree stays on `main` — read-only for implementation.

```bash
# Create
git worktree add .worktrees/<id>-<desc> -b feature/<id>-<desc>
```

## Branch Naming
Use the pattern from `.claude/config.json`:
- GitHub: `feature/<id>-<short-description>`

## Commit Format
```
<type>(<scope>): <description>

<body>

<ticket-ref>
```

Types: feat, fix, refactor, test, docs, chore
Ticket ref: `#123` (GitHub) — per config.json

## Versioning (Automatic)
Versions are bumped automatically on every push to `main` via GitHub Actions.
Commit types determine the bump — choose your type carefully:

| Commit prefix | Version bump | Example |
|---|---|---|
| `feat` | minor (1.x.0) | New skill, new agent |
| `feat!:` / `BREAKING CHANGE` | major (x.0.0) | Removed or renamed skill |
| `fix`, `refactor`, `test`, `docs`, `chore` | patch (1.0.x) | Bug fix, cleanup, docs |

The workflow updates `.claude-plugin/plugin.json` and `marketplace.json`, commits as `chore(release): v<new>`, and creates a git tag.

## PR Workflow
No hard PR size limit. 1 ticket = 1 PR targeting `main`.
Multiple commits within a PR are fine — use them to organize logical steps.
