# Project: agentwatch

Go backend (standard library).
GitHub Issues for tracking. GitHub for code and PRs.

## Critical Rules
- ALWAYS read relevant `.claude/rules/` files before working on any layer.
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.

## Rule Files
See `.claude/rules/` for conventions:
- `lessons-learned.md` — real mistakes from this codebase (authoritative, overrides assumptions)
- Other rule files as created by the team

## Build & Test

- Build: `make build`
- Test: `make test` or `go test ./...`
- Lint: `make lint` (requires golangci-lint)

## Project Structure

- `main.go` — CLI entry point, subcommand routing (`daemon`, `waybar`, `notify`)
- `plugin/` — Claude Code plugin (hooks that call `agentwatch notify`)
- `internal/daemon/` — Event-driven loop, per-window state management, rename logic, stale sweep
- `internal/detect/` — Status enum, TaskName extraction, IsStatusSymbol
- `internal/tmux/` — tmux Client interface + ExecClient implementation
- `internal/config/` — Configuration struct and defaults
- `internal/ipc/` — Event receiver socket, broadcast server/client, NDJSON state, HookEvent types
- `internal/waybar/` — Waybar custom module output (JSON formatting)

## Key Conventions

- **Interfaces**: `tmux.Client` defines the tmux boundary; implementations are swappable
- **Event-driven**: Daemon receives `HookEvent` from Claude Code hooks via Unix socket — no polling
- **Testing**: Mock-based tests in `daemon_test.go` — `mockClient` implements `tmux.Client`; tests call `handleEvent()` directly for synchronous, deterministic behavior
- **Window state**: `windowState` in daemon tracks per-window original name, original styles, original format strings, current status, pane ID, session ID, manual-name detection
- **User variables**: Daemon sets `@agentwatch-symbol` and `@agentwatch-style` per window for custom `status-format` integration; symbols are NOT embedded in window names
- **Stale sweep**: Periodic `sweepStale()` cleans up sessions whose pane no longer exists (handles Claude crashes)
