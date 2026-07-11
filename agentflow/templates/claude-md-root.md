<!-- Single-project template. For monorepos, use claude-md-root-monorepo.md instead. -->

# Project: <name>

<backend-stack> backend + <frontend-stack> frontend.
<ticket-system> for tracking. <pr-system> for code and PRs.

## Critical Rules
- ALWAYS read relevant `docs/` files when working in their topic area (e.g., `docs/git-workflow.md` before commits/PRs).
- Test-first: integration tests that assert behavior, not implementation details.
- No secrets, credentials, or API keys in code.
- No PII or stack traces in user-facing error responses.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.

<!-- IF pencil.enabled -->
## Design Files
- Design spec: `<designPath>/DESIGN.md` — screens, components, tokens, naming conventions
- Design file: `<designPath>/<name>.pen` — open in Pencil, read with Pencil MCP tools
- ALWAYS read DESIGN.md before implementing any frontend feature
<!-- END IF -->

## Reference Docs
On-demand topic docs live in `docs/` at the repo root. Read the file matching your work area:
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- Additional `docs/<topic>.md` files may be created over time as conventions emerge.

`.claude/rules/` is reserved for files explicitly `@`-imported by this CLAUDE.md (auto-loaded at session start). Don't put reference docs there.
