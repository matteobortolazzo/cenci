# claude-tools

Monorepo for Claude Code plugins and development tooling.
GitHub Issues for tracking. GitHub for code and PRs.

## Projects

- `ccflow/` — Claude Code plugin: markdown skills, agents, shell hooks
- `muxwatch/` — Go binary + Claude Code plugin: tmux session monitoring
- `dev-sandbox/` — Docker/Podman container for isolated Claude Code sessions

Each project has its own `.claude/CLAUDE.md` with project-specific context.

## Critical Rules
- ALWAYS read the relevant project's `.claude/rules/` files before working on any layer.
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.

## Build & Test

### muxwatch
- Build: `cd muxwatch && make build`
- Test: `cd muxwatch && make test` or `cd muxwatch && go test ./...`
- Lint: `cd muxwatch && make lint`

### ccflow
- No build step (markdown/shell plugin)

## Versioning

Each plugin versions independently:
- ccflow: auto-bumped on push to main (paths: `ccflow/**`), tags: `ccflow/v*`
- muxwatch: auto-bumped on push to main (paths: `muxwatch/**`), tags: `muxwatch/v*`
- sandbox: auto-bumped on push to main (paths: `dev-sandbox/**`), tags: `sandbox/v*`
