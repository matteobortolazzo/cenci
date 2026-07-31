# Test Isolation: Environment Variable Gates

Guidance for testing code that branches on ambient process/container state (e.g., environment variables).

## Rules

- **When adding env-var-gated early returns, audit ALL existing tests for env-var isolation.** When you introduce a new early-return or behavioral branch that checks an environment variable (e.g., `CENCI_SANDBOX=1`), do not assume that only new tests for that behavior need to isolate the env var. Existing tests that exercise the *un-gated* code path must be reviewed and explicitly isolated (e.g., with `t.Setenv("VAR_NAME", "")`) if that env var is ambient (always or often set) in this project's normal dev environment. A test can pass despite no longer exercising the code it was written to test, if the ambient environment satisfies the new gate's short-circuit condition — passing assertions are not sufficient evidence that the test reached its target code. Verify test coverage by timing, call counts, or explicit markers (e.g., `t.Logf()` assertions), especially for env vars set by default in the container-based dev sandbox. Ticket #202: `CENCI_SANDBOX=1` gate was added to `EnsureRunning()`, silently breaking two pre-existing tests (`TestEnsureRunningSkipsSpawnWhenAlive`, `TestEnsureRunningGivesUpAfterTimeout`) because they inherited the ambient `CENCI_SANDBOX=1` from the dev container and returned early without exercising the behavior they claimed to test (only caught during code-review pass, not by the implementer).

## Ambient daemon socket isolation

**Context**: `dispatch status`/`dispatch loop` tests assert `daemon_running: false`, but the CLI resolves the daemon socket via `watch.DefaultSocketPath()` (which honors `$XDG_RUNTIME_DIR`) with no override — so if a real cenci daemon happens to be running on the dev/CI machine, these tests dial it instead of finding nothing, and fail.

**Rule**: Any test asserting `daemon_running: false` (or otherwise depending on no daemon being reachable) must redirect socket resolution to an isolated, empty temp directory as its first statement — either via the package's `useTempSocketDir(t)` helper (see `main_test.go`, `internal/daemon/ensure_test.go`) or, where no such helper exists yet, directly with `t.Setenv("XDG_RUNTIME_DIR", t.TempDir())`. `t.TempDir()` is created 0700, satisfying `secureSocketDir()`'s permission check (no fallback to `/tmp`). Don't call `t.Parallel()` in tests that use `t.Setenv`.

## Ambient babysit state isolation

**Context**: `cenci close` consults `cenci babysit`'s supervisor state under `$XDG_STATE_HOME/cenci/babysit` before killing a window (#787), so a real supervisor running on the dev/CI machine can flip a close decision to a babysit skip.

**Rule**: Any test asserting a `cenci close` decision must run the binary with `XDG_STATE_HOME` pointed at an empty temp directory (`close_test.go`'s `emptyStateHome(t)` helper). Isolating pre-existing tests that exercise the *un-guarded* path matters as much as isolating the new guard tests. The same reasoning applies inside `internal/daemon`, whose default guard reads the same directory — `newTestDaemon` disables it outright.

## File-persisted fixture state isolation

**Context**: Test fixtures that persist state to a JSON or other file and allow multiple subprocess helpers or concurrent code to read-modify-write that file can silently drop concurrent mutations when no locking is present. A last-writer-wins race is especially dangerous for negative/absence assertions ("this field must not be present"), which fail open when a concurrent mutation gets lost.

**Rule**: Any test fixture that persists state to a file and has multiple helpers (re-exec'd subprocesses, goroutines, async operations) read-modify-write that file must use explicit file-level locking during the load-mutate-save cycle. Use `syscall.Flock` on a sibling `.lock` file (see `chainfake_test.go`'s `withChainWorldLock` pattern for a reference implementation) to serialize access. Without locking, concurrent `gh` calls (e.g., `CollectTickets` querying multiple repos), dispatcher/pipeline/babysit independent invocations, or future refactors that add concurrency will silently lose updates and make assertions pass when they should fail (#855).
