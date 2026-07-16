<!-- Single-project template. For monorepos use agents-md-root-monorepo.md. -->

# Project: <name>

<backend-stack> backend + <frontend-stack> frontend.
<ticket-system> for tracking. <pr-system> for code and PRs.

## Critical Rules

- Read the relevant `docs/` file before working in its topic area.
- Test first; assert behavior rather than implementation details.
- Never commit secrets, credentials, API keys, or PII.
- Keep tickets well scoped: one ticket equals one pull request.
- Use feature worktrees; never implement directly in the main worktree.

## Reference Docs

- `docs/git-workflow.md` — branching, commits, pull requests, and versioning.
- Additional topic guidance lives in `docs/<topic>.md` and is read on demand.

<!-- IF pencil.enabled -->
## Design Files

- Design spec: `<designPath>/DESIGN.md`
- Pencil file: `<designPath>/<name>.pen`
<!-- END IF -->

<!-- IF sandbox.enabled -->
## Sandbox Image

- `.cenci/Dockerfile` is the reviewed, repository-specific sandbox image.
<!-- END IF -->
