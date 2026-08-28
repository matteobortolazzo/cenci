<!-- Single-project template. For monorepos use agents-md-root-monorepo.md. -->

# Project: <name>

<backend-stack> backend + <frontend-stack> frontend.
<ticket-system> for tracking. <pr-system> for code and PRs.

## Critical Rules

- Read the relevant `docs/` file before working in its topic area.
- Test first; assert behavior rather than implementation details.
- Never commit secrets, credentials, API keys, or PII.
- Never expose PII or stack traces in user-facing errors.
- Keep tickets well scoped: one ticket equals one pull request.
- Use feature worktrees; never implement directly in the main worktree.

## Reference Docs

- `docs/git-workflow.md` — branching, commits, pull requests, and versioning.
- Additional topic guidance lives in `docs/<topic>.md` and is read on demand.

<!-- IF frontend project -->
## UI Conventions

- Component library: `<path-to-component-library>`
- Component catalog: `<storybook-like-app-path>` — run with `<catalog-command>`
- Reuse an existing component before authoring a new one. Browse the catalog first;
  only add a component when nothing there covers the need, and put it in the library
  rather than beside the feature that needed it.
<!-- END IF -->

<!-- IF pencil.enabled -->
## Design Files

- Design spec: `<designPath>/DESIGN.md`
- Pencil file: `<designPath>/<name>.pen`
<!-- END IF -->

<!-- IF sandbox.enabled -->
## Sandbox Image

- `.cenci/Dockerfile` is the reviewed, repository-specific sandbox image. Rebuild it
  with `cenci sandbox build` after stack or Dockerfile changes.
<!-- END IF -->
