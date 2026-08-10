# Project: watch

Go backend (standard library).
GitHub Issues for tracking. GitHub for code and PRs.

## Critical Rules
- Narrow a match-miss exclusion to the exact case being excluded — never broaden it into a silent "no match → discard" catch-all; keep the original visible fallback for every other case and add a regression test for the non-matching case.
- When changing a literal value (exec flag, environment variable, version) referenced in comments, grep the whole file for all references to that literal rather than relying on a change plan's enumerated sites — doc comments and docstrings can reference the same value and go stale (#357).
- When adding a plugin package with a version-pinned manifest, register its path in `.github/workflows/watch-version-bump.yml`'s `plugin-json-paths` list — grep that file for the existing pattern. Unregistered manifests are never bumped and stay frozen at creation-time version (#488).
- Verify every caller of a precondition-dependent path uses the right constructor variant — `NewForLaunch` leaves `Engine.Runtime` unset for deferred re-resolution, `New` sets it eagerly, and `Engine.Launch` needs the former. A shared helper can hide a misclassified caller (#585).
- When adding subprocess calls in `internal/dispatch`/`internal/babysit`, mirror `mainsync.go`'s timeout/WaitDelay/stderr conventions, not bare `exec.Command`; use each package's own `execGh` helper (`gh.go`) for `gh` calls (#825, #852, #854).

## Rule Files

Repo-wide:
- `<repo-root>/docs/cli-conventions.md` — CLI grammar, alias, env-var, and naming conventions (read before touching any user-facing command surface).
- `<repo-root>/docs/error-codes.md` — structured error-identifier (`CENCI-*`) naming convention and registered-code index (read before adding or wiring a new error code; the registry lives in `internal/errcode`).

Project topic docs — read the one matching your work area:
- `watch/docs/error-handling.md` — sentinel errors, failure classification, failure visibility, default-deny defaults, partial-result contracts
- `watch/docs/go-gotchas.md` — Go/stdlib traps: nil-slice JSON, `bufio.Reader` reuse, `os.IsNotExist` vs `os/exec`, regex splicing, closed-set enums
- `watch/docs/test-strategy.md` — verifying test assumptions, fixture/fake discipline, direct coverage for unplanned changes
- `watch/docs/test-isolation.md` — env-var gates and ambient daemon-socket isolation in tests
- `watch/docs/hook-events.md` — implementing and testing new `ipc.HookEvent` fields
- `watch/docs/tmux.md` — non-obvious tmux command behavior
- `watch/docs/dispatch-reconcile.md` — bounded-retry counter lifecycle in `internal/dispatch`
- `watch/docs/javascript.md` — JXA / JavaScript frontend patterns

`.claude/rules/` is reserved for files explicitly imported by this AGENTS.md. It is not used today — lessons route to `watch/docs/<topic>.md` or the Critical Rules above (see `flow/agents/lessons-collector.md`).

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
  - `dispatch_cmd.go` — `dispatch` (enroll/unenroll/status/loop/plan-refined) + state rendering
  - `automerge_cmd.go` — `automerge on|off|status` (fleet-wide `automerge.enabled` toggle + informational repo policy summary)
  - `status_cmd.go` — human `status` + `widget-json` (hidden alias `waybar`) + render helpers
  - `close_cmd.go` — `close` + decision rendering
  - `sandbox_cmd.go` — `sandbox build|build-base|prune|update-agent|update-plugins|reseed-creds|reap-orphans|ls|stop`: flag parsing, usage errors (exit 2), and dispatch into `internal/sandbox` + `internal/sandbox/launcher`
  - `open_cmd.go` — `open` (interactive sandbox launch, shortcut/model resolution)
  - `pipeline_cmd.go` — `pipeline` (six stage transitions: prepare/plan/await-input/execute/review/finalize) + `pipeline_mechanics_cmd.go` (label/worktree/worktree-cleanup/artifact), `pipeline_plan_cmd.go` (plan-check), `pipeline_reset_cmd.go` (reset)
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
- `internal/dispatch/` — pickup gate engine: `Ticket`/`Decision` types, the pure `Decide` gate chain, the `CollectTickets` gh+filesystem collector, the once-per-pass local-`main` sync (`mainsync.go`: `git fetch` + fast-forward-only merge, gated to `main`-only checkouts), the ticket dependency gate (`nativedeps.go`: GitHub-native `blockedBy` link conversion with inline state and no extra gh calls, same-repo URL validation, and the union merge with the legacy path; `dependency.go`: legacy `Depends on #N` body parsing, open-set/`gh issue view` resolution, per-pass call budget — retained to keep gating tickets refined before native links), the bounded cursor-paginated open-PR inventory probe (`openpr.go`: `gh api graphql`, page/record bounds, `OpenPRProbe` completeness verdict), the reconciler, and `Config` serialization
- `internal/pipeline/` — pipeline state machine and artifact storage: the `Stage` enum and `stageOrder` total order, flock-guarded transitions, `GetArtifacts`/`SetArtifacts`, worktree and label mechanics, plan-file discovery/validation, `Reset`, plan-file stage adoption (`adopt.go`: lets `plan --approve` succeed on a pre-stage-tracking ticket that has a valid `.plans/<id>-*.md` on disk), worktree attach/reuse (`AttachWorktree`: validates an existing worktree against `git worktree list --porcelain` and records it without creating anything), and the `.cenci/pipeline/<id>.json` format

## Key Conventions

- **Interfaces**: `tmux.Client` defines the tmux boundary; `frontend.Frontend` is the seam the daemon injects at startup; implementations are swappable
- **Event-driven**: Daemon receives `HookEvent` from Claude Code hooks via Unix socket — no polling
- **Session-keyed state**: Daemon keys sessions by agent session id (fallback `pane:<id>` when only a pane is known); delegates all window work to the injected `frontend.Frontend`
- **Testing**: Behavior-driven tests across the frontend seam — the daemon suite wires a real tmux frontend over `tmuxtest.MockClient` and calls `handleEvent()`/`runSweep()` directly for synchronous, deterministic behavior; assertions target renames, window options, `WindowInfo`, and core session state (not internals)
- **Window state**: `windowState` in `internal/frontend/tmux` tracks per-window original name, original styles, original format strings, current status, pane ID, session ID, manual-name detection
- **User variables**: tmux frontend sets `@cenci-symbol` and `@cenci-style` per window for custom `status-format` integration; symbols are NOT embedded in window names. It also sets `@cenci-headroom-<agent>` (a global, session-wide option, not per-window) with each agent-type's remaining budget headroom as an integer percent
- **Stale sweep**: Two mechanisms — the tmux frontend's `Sweep()` cleans up tmux-backed sessions whose pane no longer exists; the daemon's paneless TTL sweep expires sessions without a pane after `-session-ttl` (default `2h`). A pane-gone sweep pass and daemon startup each trigger one coalesced `cenci sandbox reap-orphans` self-exec (via the injectable `internal/reap.Reaper` seam) to kill orphaned container-side agent processes.
