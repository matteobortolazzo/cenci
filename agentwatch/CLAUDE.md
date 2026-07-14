# Project: agentwatch

Go backend (standard library).
GitHub Issues for tracking. GitHub for code and PRs.

## Critical Rules
- ALWAYS read relevant `.claude/rules/` files before working on any layer.
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.
- When excluding one specific known case from a parsing/filtering loop via a match-miss condition, narrow the skip to match only that case exactly — don't broaden a targeted exclusion into a silent "no match → discard" catch-all. Compute the expected case and skip only on an exact match; keep the original fallback behavior (e.g. a visible `'none'`/default state) for every other non-match, and cover the non-matching case with a regression test. Silent failures hide bugs; visible fallback rendering catches them.

## Rule Files
See `.claude/rules/` for conventions:
- `lessons-learned.md` — real mistakes from this codebase (authoritative, overrides assumptions)
- Other rule files as created by the team

## Build & Test

- Build: `make build`
- Test: `make test` or `go test ./...`
- Lint: `make lint` (requires golangci-lint)

## Project Structure

- `main.go` — CLI entry point, subcommand routing (`daemon start|stop|restart|status`, human `status`, `widget-json` (hidden alias `waybar`), `notify`)
- `plugin/` — Claude Code plugin (hooks that call `agentwatch notify`)
- `internal/daemon/` — Session-keyed event loop, hook→status mapping, paneless TTL sweep; delegates window work via `frontend.Frontend`
- `internal/frontend/` — Seam types: `SessionState`, `Frontend` interface, `Observations`, `SweepAction`, `WindowInfo`; shared name sanitizers
- `internal/frontend/tmux/` — Interactive tmux frontend: window rename/style/restore, pane-based stale sweep, renumber migration, idle-title detection
- `internal/frontend/status/` — Read-only status JSON output (Waybar custom module protocol); consumed by `agentwatch status`, noctalia, and dms
- `internal/detect/` — Status enum, TaskName extraction, IsStatusSymbol
- `internal/tmux/` — tmux Client interface + ExecClient implementation
- `internal/tmux/tmuxtest/` — Shared tmux mock for tests
- `internal/config/` — Configuration struct and defaults
- `internal/ipc/` — Event receiver socket, broadcast server/client, NDJSON state, HookEvent types
- `internal/reap/` — `Reaper` seam + `ExecReaper`: single-flight, non-blocking trigger for `agent-sand --reap-orphans`

## Key Conventions

- **Interfaces**: `tmux.Client` defines the tmux boundary; `frontend.Frontend` is the seam the daemon injects at startup; implementations are swappable
- **Event-driven**: Daemon receives `HookEvent` from Claude Code hooks via Unix socket — no polling
- **Session-keyed state**: Daemon keys sessions by agent session id (fallback `pane:<id>` when only a pane is known); delegates all window work to the injected `frontend.Frontend`
- **Testing**: Behavior-driven tests across the frontend seam — the daemon suite wires a real tmux frontend over `tmuxtest.MockClient` and calls `handleEvent()`/`runSweep()` directly for synchronous, deterministic behavior; assertions target renames, window options, `WindowInfo`, and core session state (not internals)
- **Window state**: `windowState` in `internal/frontend/tmux` tracks per-window original name, original styles, original format strings, current status, pane ID, session ID, manual-name detection
- **User variables**: tmux frontend sets `@agentwatch-symbol` and `@agentwatch-style` per window for custom `status-format` integration; symbols are NOT embedded in window names. It also sets `@agentwatch-headroom-<agent>` (a global, session-wide option, not per-window) with each agent-type's remaining budget headroom as an integer percent
- **Stale sweep**: Two mechanisms — the tmux frontend's `Sweep()` cleans up tmux-backed sessions whose pane no longer exists; the daemon's paneless TTL sweep expires sessions without a pane after `-session-ttl` (default `2h`). A pane-gone sweep pass and daemon startup each trigger one coalesced `agent-sand --reap-orphans` call (via the injectable `internal/reap.Reaper` seam) to kill orphaned container-side agent processes.
