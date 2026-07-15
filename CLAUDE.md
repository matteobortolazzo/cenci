# cenci

One cenci product implemented as a monorepo of client plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `flow/` — workflow layer: Claude Code pipeline plus portable Codex conventions
- `watch/` — attention layer: Go daemon and native Claude Code/Codex hooks
- `sandbox/` — isolation layer: Docker/Podman launcher for Claude Code and Codex

Each project has its own `CLAUDE.md` with project-specific context.

## Critical Rules
- ALWAYS read the relevant project's `CLAUDE.md` — plus its `.claude/rules/` files where they exist (currently only `watch/`) — before working on any layer.
- CLI grammar, alias, env-var, and runtime-object naming conventions live in `docs/cli-conventions.md` — read it before adding or changing any user-facing command surface.
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- ALWAYS work in a git worktree — for any change (code, docs, config), not just feature work. Never modify files in the main worktree.
- Deliver every change as a PR unless told otherwise: commit in the worktree, push the branch, open a PR. Never commit directly to main.
- Refactor phases must not override decisions the approved plan explicitly reasoned about — if renaming, reorganizing, or restructuring something the plan called out directly, that plan reasoning is binding unless refactoring reveals a fundamental error.

## Build & Test

### watch
- Build: `cd watch && make build`
- Test: `cd watch && make test` or `cd watch && go test ./...`
- Lint: `cd watch && make lint`

### flow
- No build step (markdown/shell plugin)

### sandbox and installer
- Syntax: `bash -n install.sh sandbox/entrypoint.sh`
- Tests: `bash sandbox/tests/installer-clients.test.sh` and the other host-runnable suites in `sandbox/tests/`

## Versioning

Each plugin versions independently:
- flow: auto-bumped on push to main (paths: `flow/**`), tags: `flow/v*`
- watch: auto-bumped on push to main (paths: `watch/**`), tags: `watch/v*`
- sandbox: auto-bumped on push to main (paths: `sandbox/**`), tags: `sandbox/v*`
