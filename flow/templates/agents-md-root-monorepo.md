# Project: <name>

Monorepo with <N> projects. <ticket-system> for tracking. <pr-system> for pull requests.

## Critical Rules

- Read the applicable project `AGENTS.md` and relevant root `docs/` guidance.
- Test first; assert behavior rather than implementation details.
- Never commit secrets, credentials, API keys, or PII.
- Never expose PII or stack traces in user-facing errors.
- Keep tickets well scoped: one ticket equals one pull request.
- Use feature worktrees; never implement directly in the main worktree.

## Projects

| Directory | Stack | Description |
|---|---|---|
| `<path>` | <stack> | <description> |

## Reference Docs

- `docs/git-workflow.md` — branching, commits, pull requests, and versioning.

<!-- IF sandbox.enabled -->
## Sandbox Image

- Rebuild `.cenci/Dockerfile` with `cenci sandbox build` after any project stack changes.
<!-- END IF -->
