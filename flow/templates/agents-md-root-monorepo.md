# Project: <name>

Monorepo with <N> projects. <ticket-system> for tracking. <pr-system> for pull requests.

## Critical Rules

- Read the applicable project `AGENTS.md` and relevant root `docs/` guidance.
- Test first; assert behavior rather than implementation details.
- Never commit secrets, credentials, API keys, or PII.
- Never expose PII or stack traces in user-facing errors.
- Keep tickets well scoped: one ticket equals one pull request.
- Use feature worktrees; never implement directly in the main worktree.
- When a step does not need the user's input, keep going and put status notes in the same message as the next action. Stop and ask only when you cannot continue without the user, or before anything destructive: deleting data, force-pushing, or changing anything outside this repository.

## Projects

| Directory | Stack | Description |
|---|---|---|
| `<path>` | <stack> | <description> |

## Reference Docs

- `docs/git-workflow.md` — branching, commits, pull requests, and versioning.

<!-- IF any frontend project -->
## UI Conventions

- Component library: `<path-to-component-library>`
- Component catalog: `<storybook-like-app-path>` — run with `<catalog-command>`
- Reuse an existing component before authoring a new one. Browse the catalog first;
  only add a component when nothing there covers the need, and put it in the library
  rather than beside the feature that needed it.
- Project-specific UI rules live in each frontend project's `AGENTS.md`.
<!-- END IF -->

<!-- IF sandbox.enabled -->
## Sandbox Image

- Rebuild `.cenci/Dockerfile` with `cenci sandbox build` after any project stack changes.
<!-- END IF -->
