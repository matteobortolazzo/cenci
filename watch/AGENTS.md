# Project: watch

Go backend (standard library).
GitHub Issues for tracking. GitHub for code and PRs.

## Critical Rules
- Test-first: integration tests that assert behavior, not implementation details.
- Keep tickets well-scoped. 1 ticket = 1 PR.
- Use git worktrees for all feature work. Never modify code in main worktree.
- Narrow a match-miss exclusion to the exact case being excluded — never broaden it into a silent "no match → discard" catch-all; keep the original visible fallback for every other case and add a regression test for the non-matching case.
- When changing a literal value (exec flag, environment variable, version) referenced in comments, grep the whole file for all references to that literal rather than relying on a change plan's enumerated sites — doc comments and docstrings can reference the same value and go stale (#357).
- `os.IsNotExist`/`errors.Is(err, fs.ErrNotExist)` only classify filesystem-call errors — they never match an `os/exec` error. To handle a missing input file for an external command, `os.Stat` the path yourself first; don't infer it from the command's exit error.

## Rule Files
CLI grammar, alias, env-var, and naming conventions: `<repo-root>/docs/cli-conventions.md` (read before touching any user-facing command surface).
`.claude/rules/` is reserved for files explicitly imported by this AGENTS.md. It is not used today — lessons route to `docs/<topic>.md` or the Critical Rules above (see `flow/agents/lessons-collector.md`).

## Build & Test

- Build: `make build`
- Test: `make test` or `go test ./...`
- Lint: `make lint` (requires golangci-lint)
- Reap contract suite: `make build && CENCI_BIN=$PWD/cenci bash tests/reap-orphans.test.sh` (mock docker/podman/tmux; pins the `cenci sandbox reap-orphans` scan/kill/escalation behavior)

## Project Structure

- `main.go` — CLI entry point: imports, version, `socket-dir`, top-level dispatch to each command group below, doctor/update wrapper-mode delegation; also handles the `cn` argv[0] alias (routes to `open`) and the `cenci-sand` argv[0] tombstone (prints a migration map, exits 2). All command-group implementations live in their own `*_cmd.go` file (same `package main`):
  - `daemon_cmd.go` — `daemon start|stop|restart|status` + PID-file management
  - `notify_cmd.go` — `notify` (hook event ingestion)
  - `run_cmd.go` — `run` (dispatch a workflow into a new tmux window)
  - `dispatch_cmd.go` — `dispatch` (enroll/unenroll/status/loop) + state rendering
  - `status_cmd.go` — human `status` + `widget-json` (hidden alias `waybar`) + render helpers
  - `close_cmd.go` — `close` + decision rendering
  - `sandbox_cmd.go` — `sandbox build|build-base|prune|update-agent|update-plugins|reseed-creds|reap-orphans|ls|stop`: flag parsing, usage errors (exit 2), and dispatch into `internal/sandbox` + `internal/sandbox/launcher`
  - `open_cmd.go` — `open` (interactive sandbox launch, shortcut/model resolution)
- `plugin/` — Claude Code plugin (hooks that call `cenci notify`)
- `internal/daemon/` — Session-keyed event loop, hook→status mapping, paneless TTL sweep; delegates window work via `frontend.Frontend`
- `internal/frontend/` — Seam types: `SessionState`, `Frontend` interface, `Observations`, `SweepAction`, `WindowInfo`; shared name sanitizers
- `internal/frontend/tmux/` — Interactive tmux frontend: window rename/style/restore, pane-based stale sweep, renumber migration, idle-title detection
- `internal/frontend/status/` — Read-only status JSON output (Waybar custom module protocol); consumed by `cenci status`, noctalia, and dms
- `internal/detect/` — Status enum, TaskName extraction, IsStatusSymbol
- `internal/tmux/` — tmux Client interface + ExecClient implementation
- `internal/tmux/tmuxtest/` — Shared tmux mock for tests
- `internal/config/` — Configuration struct and defaults
- `internal/ipc/` — Event receiver socket, broadcast server/client, NDJSON state, HookEvent types
- `internal/reap/` — `Reaper` seam + `ExecReaper`: single-flight, non-blocking self-exec of `cenci sandbox reap-orphans`
- `internal/sandbox/` — shared sandbox primitives and the SOURCE OF TRUTH for CLI tables: runtime detection (podman, then docker), the ch/cs/co/cf + xl/xt/xs shortcut tables, the `claude-cenci-`/`codex-cenci-` name-prefix pattern, and native container listing/stopping for `sandbox ls`/`sandbox stop`
- `internal/sandbox/launcher/` — the native launch engine (ported from the retired `sandbox/cenci-sand` bash launcher): asset-dir resolution (`CENCI_SANDBOX_ASSETS` override → marketplace → plugin cache), byte-exact BASE_TAG content hash, repo scoping, image builds, plugin updates, interactive launch (in-process cenci wiring, RUN_ARGS, readiness poll, syscall.Exec attach), prune, and the orphan reaper (verbatim in-container scan/liveness scripts)

## Key Conventions

- **Interfaces**: `tmux.Client` defines the tmux boundary; `frontend.Frontend` is the seam the daemon injects at startup; implementations are swappable
- **Event-driven**: Daemon receives `HookEvent` from Claude Code hooks via Unix socket — no polling
- **Session-keyed state**: Daemon keys sessions by agent session id (fallback `pane:<id>` when only a pane is known); delegates all window work to the injected `frontend.Frontend`
- **Testing**: Behavior-driven tests across the frontend seam — the daemon suite wires a real tmux frontend over `tmuxtest.MockClient` and calls `handleEvent()`/`runSweep()` directly for synchronous, deterministic behavior; assertions target renames, window options, `WindowInfo`, and core session state (not internals)
- **Window state**: `windowState` in `internal/frontend/tmux` tracks per-window original name, original styles, original format strings, current status, pane ID, session ID, manual-name detection
- **User variables**: tmux frontend sets `@cenci-symbol` and `@cenci-style` per window for custom `status-format` integration; symbols are NOT embedded in window names. It also sets `@cenci-headroom-<agent>` (a global, session-wide option, not per-window) with each agent-type's remaining budget headroom as an integer percent
- **Stale sweep**: Two mechanisms — the tmux frontend's `Sweep()` cleans up tmux-backed sessions whose pane no longer exists; the daemon's paneless TTL sweep expires sessions without a pane after `-session-ttl` (default `2h`). A pane-gone sweep pass and daemon startup each trigger one coalesced `cenci sandbox reap-orphans` self-exec (via the injectable `internal/reap.Reaper` seam) to kill orphaned container-side agent processes.
