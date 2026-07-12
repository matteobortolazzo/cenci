# agent-stack

Monorepo for Claude Code plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `agentflow/` — Claude Code plugin: markdown skills, agents, shell hooks
- `agentwatch/` — Go binary + Claude Code plugin: coding-agent session monitoring (tmux, waybar, noctalia, DMS)
- `dev-sandbox/` — Docker/Podman container for isolated Claude Code sessions

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

## Versioning

Each plugin versions independently:
- agentflow: auto-bumped on push to main (paths: `agentflow/**`), tags: `agentflow/v*`
- agentwatch: auto-bumped on push to main (paths: `agentwatch/**`), tags: `agentwatch/v*`
- agent-sandbox: auto-bumped on push to main (paths: `dev-sandbox/**`), tags: `agent-sandbox/v*`
