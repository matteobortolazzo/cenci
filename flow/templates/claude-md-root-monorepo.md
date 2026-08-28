# Project: <name>

Monorepo with <N> projects. <ticket-system> for tracking. <pr-system> for PRs.

## Critical Rules
- ALWAYS read the CLAUDE.md in the project directory you are working in.
- Read relevant `docs/` files when working in their topic area (e.g., `docs/git-workflow.md` before commits/PRs).
- Test-first: integration tests that assert behavior, not implementation details.
- No secrets, credentials, or API keys in code.
- No PII or stack traces in user-facing error responses.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.

## Projects
| Directory | Stack | Description |
|-----------|-------|-------------|
| `<path>` | <stack> | <description> |

<!-- IF sandbox.enabled -->
## Sandbox Image
- `.cenci/Dockerfile` — committed, single per-repo image covering the union of every project's stack; the whole team builds the same image
- Rebuild after changing any project's stack or the Dockerfile: `cenci sandbox build` (run from inside this repo)
<!-- END IF -->

## Reference Docs
On-demand topic docs live in `docs/` at the repo root. Read the file matching your work area:
- `docs/git-workflow.md` — branching, commits, PRs, versioning
- Additional `docs/<topic>.md` files may be created over time as conventions emerge.

`.claude/rules/` is reserved for files explicitly `@`-imported by this CLAUDE.md (auto-loaded at session start). Don't put reference docs there.
