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
- When introducing a new sentinel error for detection via `errors.Is()`, add a direct unit test at the package boundary asserting the sentinel is returned and detectable — do not rely on indirect coverage via higher-level integration tests, as a refactor could silently break `errors.Is()` while indirect tests still pass (#412).
- When testing error handling where the same code path can return multiple distinct failure classes, use error-content-specific assertions (e.g., `strings.Contains(err, "specific-marker")`) for each case, not just empty/non-empty checks. A non-empty check alone would pass identically if a regression collapsed multiple classes into the same placeholder string, allowing the tests to silently stop distinguishing between them (#446).
- When adding a new plugin package with a version-pinned manifest file, register its path in `.github/workflows/watch-version-bump.yml`'s `plugin-json-paths` list — grep that file for existing plugin manifest entries to see the full pattern. Without this registration, the version-bump CI workflow won't update the new manifest on release, and it stays frozen at its creation-time version forever (#488).
- When a test infrastructure change (new environment variable, mock parameter) requires updating multiple mock implementations or test helpers, grep for ALL of them before committing—in particular, fake `docker inspect` / `image inspect` fixtures appear across multiple test files (`engine_test.go`: buildEngine, volumeCheckEngine; `sandbox_open_test.go`: writeScriptedRuntime, openTestEnv, batchEnv) and must stay in sync. Missing even one location can silently cause end-to-end tests to exercise different code paths than unit tests, even when both pass (#493).
- When implementing a cleanup or prune function that depends on frontend state (e.g., `WindowInfo()`) which a concurrent sweep or teardown operation will invalidate, capture that state before calling the mutating operation, not after — `Sweep()` and `forgetWindow()` tear down the frontend's session/pane tracking, making references unavailable for post-operation cleanup. Add explicit tests for all session-closure paths (SessionEnd-driven, pane-gone sweep), not just the primary case, to verify the cleanup actually executes and mitigates its intended risk (#522).
- When building a regex or matcher pattern by splicing strings from a data-driven collection (e.g., `SupportedAgents`), guard against empty collections with an init-time panic (an empty slice silently degenerates to mismatches or over-match in deletion-critical paths like `sandbox prune`), and escape each element with `regexp.QuoteMeta` before joining (unescaped metacharacters in a future element would silently broaden the match instead of erroring). See `sandbox.go`'s `sandboxNamePattern`, `homeVolumePattern`, and `agentCLIVolumePattern` builders (#528).
- When extracting or reusing a helper function in a new caller with different error stakes (e.g., from informational use to decision-gating), re-examine the helper's error-handling contract even if the extraction preserved existing behavior and tests—a silent-on-error default acceptable in a low-stakes context may mask errors in higher-stakes decision paths (#560).
- When a diagnostic or collection function calls multiple independent read helpers (image metadata, plugin manifests, logs, container inspection), all helpers must use the same failure-visibility strategy — either all show unknown/unavailable placeholders and append `SeverityWarning` findings on read failure, or all silently omit sections. Mixing patterns makes the report ambiguous: readers cannot distinguish 'read failed' from 'no data to display.' See `Diagnose()` in `watch/internal/sandbox/launcher/diagnose.go` for the `versionOrUnknown` pattern (#572).
- When implementing error-type classification for a code path that can fail in multiple distinct ways (e.g., an `os/exec` command failing either because 'no such container' or 'daemon unreachable'), check the same package for existing classification patterns (e.g., `errors.As(err, &exitErr)`, type assertions on `*exec.ExitError`, or sentinel-error checks) before inventing new error handling. Reusing patterns reduces code divergence and prevents silent classification breakage during future refactors — see `readHomeVolumeFile` in `watch/internal/sandbox/launcher/launch.go` and `containerStartupState` in `diagnose.go` (#572).
- When a shared helper or constructor variant wraps logic with precondition-dependent behavior (e.g., leaving `Engine.Runtime` unset for deferred re-resolution in `NewForLaunch` vs eagerly setting it in `New`), verify that all callers of the downstream code path that depends on the precondition (e.g., `Engine.Launch`) use the correct constructor variant—don't rely on the plan's verb categorization alone, as a shared helper can hide that one caller was misclassified. In #585, `cenci sandbox reseed-creds` was documented as an open-family alias but used `newEngine()` (eager `New()`) instead of `launcher.NewForLaunch()` (deferred), silently breaking dind runtime re-selection even though Docker was present, because the constructor precondition (leaving Runtime unset) was never set.
- When returning a slice from a helper that can be empty (e.g., slice-building helpers in JSON-serialization code paths), initialize with `make([]T, 0)` rather than `var s []T` or leaving it nil — Go's `json.Marshal` serializes nil slices as `null`, not `[]`, breaking stable JSON contracts (e.g., `cenci audit --json` where the default-safe case has no boundary weakenings). Initialize with `make([]T, 0, cap)` to ensure empty results marshal as an empty array `[]`. Add regression tests asserting the absence of `:null` in JSON output and presence of `:[]` for empty cases (see `watch/internal/sandbox/launcher/audit_test.go`, lines 566–576, for the pattern) (#588).
- When adding new external-command calls (e.g., `docker ps`, `docker inspect`) to a code path, audit all tests exercising that path — pre-existing tests that construct minimal fakes (e.g., `Engine{Runtime: "docker"}` without putting a fake binary on PATH) will fail when the new code attempts to invoke the command. Use `go clean -testcache` to avoid test-cache masking failures in previously-cached test packages — a `go test ./...` pass with stale cache does not validate newly-added external calls (#620).
- When refactoring code that performs side effects and changing the order of operations (e.g., moving container cleanup before vs. after validation), explicitly add a regression test and comment documenting the new ordering — do not assume existing tests will detect the behavior change. Code-review confirmation is not a substitute for a test assertion of the specific ordering (#620).

## Rule Files
CLI grammar, alias, env-var, and naming conventions: `<repo-root>/docs/cli-conventions.md` (read before touching any user-facing command surface).
Structured error-identifier (`CENCI-*`) naming convention and registered-code index: `<repo-root>/docs/error-codes.md` (read before adding or wiring a new error code; the registry lives in `internal/errcode`).
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
