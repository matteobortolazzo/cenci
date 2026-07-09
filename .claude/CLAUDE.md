# claude-tools

Monorepo for Claude Code plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `ccflow/` — Claude Code plugin: markdown skills, agents, shell hooks
- `agentwatch/` — Go binary + Claude Code plugin: coding-agent session monitoring (tmux, waybar, noctalia, DMS)
- `dev-sandbox/` — Docker/Podman container for isolated Claude Code sessions

Each project has its own `.claude/CLAUDE.md` with project-specific context.

## Critical Rules
- ALWAYS read the relevant project's `.claude/rules/` files before working on any layer.
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.

## Build & Test

### agentwatch
- Build: `cd agentwatch && make build`
- Test: `cd agentwatch && make test` or `cd agentwatch && go test ./...`
- Lint: `cd agentwatch && make lint`

### ccflow
- No build step (markdown/shell plugin)

## Versioning

Each plugin versions independently:
- ccflow: auto-bumped on push to main (paths: `ccflow/**`), tags: `ccflow/v*`
- agentwatch: auto-bumped on push to main (paths: `agentwatch/**`), tags: `agentwatch/v*`
