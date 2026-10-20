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

## Versioning (Automatic)
Each plugin versions independently. A push to `main` that touches a plugin's path bumps only that
plugin, through `.github/workflows/plugin-version-bump.yml` (the shared logic in
`.github/scripts/bump-plugin-version.sh`) called by one workflow per plugin:

| Plugin id | Path filter | Tag | Caller workflow |
|---|---|---|---|
| `cenci` (flow) | `flow/**` | `flow/v<new>` | `flow-version-bump.yml` |
| `cenci-watch` | `watch/**` | `watch/v<new>` | `watch-version-bump.yml` (then dispatches `watch-release.yml`) |
| `cenci-sandbox` | `sandbox/**` | `sandbox/v<new>` | `sandbox-version-bump.yml` (also runnable manually) |

The bump type is classified from the triggering commit only (its subject and body, which is the PR
title and body under squash-merge), so choose the type carefully. Matching is case-insensitive:

| Triggering commit | Version bump | Example |
|---|---|---|
| `feat!:`, any `<type>(<scope>)!:`, or a `BREAKING CHANGE:` / `BREAKING-CHANGE:` line | major (x.0.0) | Removed or renamed skill |
| `feat:` or `feat(<scope>):` | minor (1.x.0) | New skill, new agent |
| everything else (`fix`, `refactor`, `test`, `docs`, `chore`, a scope-less `fix!:`) | patch (1.0.x) | Bug fix, cleanup, docs |

The workflow stamps the plugin's manifests and its `marketplace.json` entry, commits as
`chore(release): <plugin-id>/v<new>` (for flow the plugin id is `cenci`, e.g.
`chore(release): cenci/v1.2.0`) and creates the git tag `<layer>/v<new>` (e.g. `flow/v1.2.0`). It
skips a triggering commit that is already that plugin's own release commit, and the callers skip
pushes by `github-actions[bot]`.

## PR Workflow
1 ticket = 1 PR targeting `main`. A PR is measured against its ticket's S/M/L size: the PR body's
`Size:` line compares the final diff with the ticket's bound, and a PR above its ticket's size is
flagged and must not be merged on green checks alone. See
[`flow/docs/ticket-sizing.md`](../flow/docs/ticket-sizing.md) for the bounds.
Multiple commits within a PR are fine — use them to organize logical steps.
