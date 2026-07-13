# agent-stack

One agent-stack product implemented as a monorepo of client plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `agentflow/` — workflow layer: Claude Code pipeline plus portable Codex conventions
- `agentwatch/` — attention layer: Go daemon and native Claude Code/Codex hooks
- `dev-sandbox/` — isolation layer: Docker/Podman launcher for Claude Code and Codex

Each project has its own `CLAUDE.md` with project-specific context.

## Critical Rules
- ALWAYS read the relevant project's `.claude/rules/` files before working on any layer.
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- ALWAYS work in a git worktree — for any change (code, docs, config), not just feature work. Never modify files in the main worktree.
- Deliver every change as a PR unless told otherwise: commit in the worktree, push the branch, open a PR. Never commit directly to main.
- Refactor phases must not override decisions the approved plan explicitly reasoned about — if renaming, reorganizing, or restructuring something the plan called out directly, that plan reasoning is binding unless refactoring reveals a fundamental error.

## Build & Test

### agentwatch
- Build: `cd agentwatch && make build`
- Test: `cd agentwatch && make test` or `cd agentwatch && go test ./...`
- Lint: `cd agentwatch && make lint`

### agentflow
- No build step (markdown/shell plugin)

### agent-sandbox and installer
- Syntax: `bash -n install.sh dev-sandbox/agent-sand dev-sandbox/entrypoint.sh`
- Tests: `bash dev-sandbox/tests/installer-clients.test.sh` and the other host-runnable suites in `dev-sandbox/tests/`

## Versioning

Each plugin versions independently:
- agentflow: auto-bumped on push to main (paths: `agentflow/**`), tags: `agentflow/v*`
- agentwatch: auto-bumped on push to main (paths: `agentwatch/**`), tags: `agentwatch/v*`
- agent-sandbox: auto-bumped on push to main (paths: `dev-sandbox/**`), tags: `agent-sandbox/v*`
