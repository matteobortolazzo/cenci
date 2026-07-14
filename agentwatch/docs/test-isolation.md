# Test Isolation: Environment Variable Gates

Guidance for testing code that branches on ambient process/container state (e.g., environment variables).

## Rules

- **When adding env-var-gated early returns, audit ALL existing tests for env-var isolation.** When you introduce a new early-return or behavioral branch that checks an environment variable (e.g., `AGENT_SAND=1`), do not assume that only new tests for that behavior need to isolate the env var. Existing tests that exercise the *un-gated* code path must be reviewed and explicitly isolated (e.g., with `t.Setenv("VAR_NAME", "")`) if that env var is ambient (always or often set) in this project's normal dev environment. A test can pass despite no longer exercising the code it was written to test, if the ambient environment satisfies the new gate's short-circuit condition — passing assertions are not sufficient evidence that the test reached its target code. Verify test coverage by timing, call counts, or explicit markers (e.g., `t.Logf()` assertions), especially for env vars set by default in the container-based dev sandbox. Ticket #202: `AGENT_SAND=1` gate was added to `EnsureRunning()`, silently breaking two pre-existing tests (`TestEnsureRunningSkipsSpawnWhenAlive`, `TestEnsureRunningGivesUpAfterTimeout`) because they inherited the ambient `AGENT_SAND=1` from the dev container and returned early without exercising the behavior they claimed to test (only caught during code-review pass, not by the implementer).

## Ambient daemon socket isolation

**Context**: `dispatch status`/`dispatch loop` tests assert `daemon_running: false`, but the CLI resolves the daemon socket via `watch.DefaultSocketPath()` (which honors `$XDG_RUNTIME_DIR`) with no override — so if a real agentwatch daemon happens to be running on the dev/CI machine, these tests dial it instead of finding nothing, and fail.

**Rule**: Any test asserting `daemon_running: false` (or otherwise depending on no daemon being reachable) must redirect socket resolution to an isolated, empty temp directory as its first statement — either via the package's `useTempSocketDir(t)` helper (see `main_test.go`, `internal/daemon/ensure_test.go`) or, where no such helper exists yet, directly with `t.Setenv("XDG_RUNTIME_DIR", t.TempDir())`. `t.TempDir()` is created 0700, satisfying `secureSocketDir()`'s permission check (no fallback to `/tmp`). Don't call `t.Parallel()` in tests that use `t.Setenv`.
