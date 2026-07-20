# Shell scripting gotchas

Narrow, easy-to-miss pitfalls in shell/jq/grep patterns used across flow's phases,
hooks, and tests.

## Rules

- **Bash tool CWD does not reliably persist across multiple calls within a single subagent session** — an initial standalone `cd <worktree-path>` call does NOT guarantee subsequent Bash calls inherit that CWD. For verification-critical commands (test runs, build commands), especially when comparing before/after state (red vs green), always use fully absolute paths or re-verify CWD before each command, rather than relying solely on an initial `cd`. A wrong-directory execution produces a highly plausible but false result (e.g., running stale tests from the main worktree while thinking you're testing the worktree's changes). See implement skill's phase-3-test-red.md and phase-4-implement-green.md Delegation Context sections.
- In jq fallback chains for field extraction (e.g., `.field1 // .field2 // empty`), never rely on bare `//` operators to skip empty strings — jq's `//` only falls back on `null` or `false`, treating `""` as a valid truthy value. Use explicit emptiness checks: `if (.field1 // "") != "" then .field1 else (.field2 // empty) end`. This prevents empty-but-present fields (e.g., from a malformed or legacy JSON payload) from short-circuiting to unintended defaults in security guards or early-exit logic, risking silent allow-through in sensitive-file checks or other fail-closed hooks.
- When writing grep-based contract tests for documentation changes, assert the specific replacement text at the edit site, never a generic marker (e.g., "git -C", "absolute path") that may already exist elsewhere in the file — generic markers pass vacuously against unfixed prose and hide regressions. See `flow/tests/subagent-cwd-contract.test.sh` for the pattern: use a `MARKER` variable with the exact replacement sentence, not a bare keyword.
