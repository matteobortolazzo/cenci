# Health gates

## What a gate is

A **gate** is an optional `gateCommand` field in `.cenci/config.json` — either
top-level (single-repo) or on a `projects[]` entry (monorepo). It's a fast
local health check: a single shell command that tells you whether a project
is currently healthy, without running a full CI pipeline.

## Why it exists

A gate is a local pre-flight — it catches a repository that's already red
*before* new work gets piled on top of it, and before that red state ever
reaches CI. Two consumers rely on it:

- The implement pipeline's Baseline Gate Check, run at the Phase 2 → Phase 3
  boundary (`flow/skills/implement/phases/phase-2-worktree.md`): before
  handing a fresh worktree off to Phase 3, it confirms the worktree's own
  baseline is green.
- `babysit` and `ci-repair` (`flow/skills/babysit/SKILL.md`,
  `flow/skills/ci-repair/SKILL.md`): a repair agent verifies a fix against the
  project's local gate before pushing, instead of relying solely on a CI
  round-trip to find out whether the fix actually worked.

## Exit-0-is-healthy contract

`gateCommand` follows the same contract everywhere it's consumed:

- Exit `0` → healthy.
- Any non-zero exit → red (unhealthy).
- Field absent, or present but an empty string → `unset` (the check is
  skipped, not treated as a failure).

## How it runs

`flow/hooks/scripts/run-gate.sh [slug]` is the single resolver every consumer
calls:

1. It reads `.cenci/config.json` and resolves the `gateCommand` — top-level
   when called with no argument, or the matching `projects[].gateCommand`
   when called with a project `slug`.
2. It `cd`s into the project's `path` (repo root for single-repo, the
   project's configured `path` for a monorepo entry).
3. It runs the resolved command via `sh -c`.
4. It reports the outcome on stdout as `GATE_STATUS=green|red|unset` and
   exits accordingly.

**Trust boundary**: the `gateCommand` string comes only from trusted,
committed `.cenci/config.json` content — never from untrusted input — so it's
executed via `sh -c` without further sanitization. The only externally
influenced input to `run-gate.sh` is the optional `slug` argument, which is
never string-interpolated into a shell command or jq program.

## Authoring guidance

Keep a gate fast — it should run the project's test suite (or an equivalently
quick check), not a full image build. This is why this repo's `sandbox` gate
explicitly excludes `tests/smoke.test.sh`: that suite triggers a full
container image build, which is far too slow for a pre-flight health check.

When dynamically discovering a file set (via `find`, glob, etc.) to loop over and execute, guard against silent false-green:

- **`xargs` invocations**: Always include the `-r` / `--no-run-if-empty` flag. Without it, if the `find` matches zero files, GNU `xargs` invokes the target command once with zero arguments. For `bash` specifically, this may silently exit 0 (reading from inherited stdin) or hang, causing a health check to report green despite running zero tests.
- **Shell loop patterns**: Explicitly verify that at least one iteration actually executed a non-skip command. Use a counter (e.g., `n=0; ... n=$((n+1)); done; [ "$n" -gt 0 ] || exit 1`) or similar guard. If the glob pattern or conditionals cause every iteration to be skipped (e.g., all matched files are excluded), the loop exits 0, falsely reporting health.

## This repo's gates (dogfooding)

This repo configures a `gateCommand` for each of its three projects in
`.cenci/config.json`:

| Project | `gateCommand` |
|---|---|
| `flow` | `find . -name '*.json' -print0 \| xargs -0 -r -n1 jq empty && find . -name '*.test.sh' -print0 \| sort -z \| xargs -0 -r -n1 bash` |
| `watch` | `make test` |
| `sandbox` | `n=0; for t in tests/*.test.sh; do [ "$t" = tests/smoke.test.sh ] && continue; bash "$t" \|\| exit 1; n=$((n+1)); done; [ "$n" -gt 0 ] \|\| exit 1` |

## See also

`flow/skills/configure/SKILL.md` — step 6, "Write `.cenci/config.json`",
documents the full config schema, including the per-project `gateCommand`
field (optional, unlike `lintCommand`, and not tied to the Stack-to-CI
mapping table).
