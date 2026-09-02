# Health gates

## What a gate is

A **gate** is an optional `gateCommand` field in `.cenci/config.json` — either
top-level (single-repo) or on a `projects[]` entry (monorepo). It's a fast
local health check: a single shell command that tells you whether a project
is currently healthy, without running a full CI pipeline.

## Why it exists

A gate is a local pre-flight — it catches a repository that's already red
*before* new work gets piled on top of it, and before that red state ever
reaches CI. Three consumers rely on it:

- The implement pipeline's Baseline Gate Check, run at the Phase 2 → Phase 3
  boundary (`flow/skills/implement/phases/phase-2-worktree.md`): before
  handing a fresh worktree off to Phase 3, it confirms the worktree's own
  baseline is green.
- `babysit` and `ci-repair` (`flow/skills/babysit/SKILL.md`,
  `flow/skills/ci-repair/SKILL.md`): a repair agent verifies a fix against the
  project's local gate before pushing, instead of relying solely on a CI
  round-trip to find out whether the fix actually worked.
- The maintain checker's `gate-command` check
  (`flow/skills/maintain/scripts/check.sh`): reports `fail` when a configured
  gate is red or missing, and `skip` (never a false `pass`) when the gate
  can't be resolved or run in the current environment (e.g. `run-gate.sh` is
  unreachable, or the gate command itself isn't found).

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
4. It captures the resolved command's combined stdout+stderr to a run-scoped
   log, prints at most `cenci.gateOutputLines` trailing lines of it (default
   `120`, top-level `.cenci/config.json` field), then reports the outcome on
   stdout as `GATE_STATUS=green|red|unset` and exits accordingly. A red gate
   additionally prints a `GATE_LOG=<absolute path>` line naming where the
   full untruncated output is retrievable; a green gate deletes the log and
   prints no `GATE_LOG=` line.

**Trust boundary**: the `gateCommand` string comes only from trusted,
committed `.cenci/config.json` content — never from untrusted input — so it's
executed via `sh -c` without further sanitization. The only externally
influenced input to `run-gate.sh` is the optional `slug` argument, which is
never string-interpolated into a shell command or jq program.

## Reading gate output

`GATE_STATUS=` and `GATE_LOG=` are position-independent, additive lines:
parse each with a last-match scan (`grep -oE '^GATE_STATUS=[a-z]+$' |
tail -n1`, mirroring `flow/skills/maintain/scripts/check.sh`'s
`check_gate_command`), never the first match — a spoofed `GATE_STATUS=`- or
`GATE_LOG=`-shaped line inside the gate command's own output can never win a
last-match parse of the real envelope, which `run-gate.sh` always prints
last.

The captured stdout is truncated to `cenci.gateOutputLines` trailing lines
(default `120`) of the gate command's combined stdout+stderr — it is not
guaranteed to be the whole thing. On a red gate, the full untruncated output
stays retrievable at the path named by `GATE_LOG=<absolute path>`; locate the
failing detail with the client's search tool (e.g. Claude Code's `Grep`,
Codex's `rg`), then read only the failing region rather than inlining the
whole log. `GATE_LOG` is red-only: a green gate deletes its log and prints no
`GATE_LOG=` line, so retention never accumulates on a healthy repo — only
under `${TMPDIR:-/tmp}/cenci/` for gates that are currently red.

## Authoring guidance

Keep a gate fast — it should run the project's test suite (or an equivalently
quick check), not a full image build. This is why this repo's `sandbox` gate
explicitly excludes `tests/smoke.test.sh`: that suite triggers a full
container image build, which is far too slow for a pre-flight health check.

When dynamically discovering a file set (via `find`, glob, etc.) to loop over and execute, guard against silent false-green:

- **`xargs` invocations**: Always include the `-r` / `--no-run-if-empty` flag. Without it, if the `find` matches zero files, GNU `xargs` invokes the target command once with zero arguments. For `bash` specifically, this may silently exit 0 (reading from inherited stdin) or hang, causing a health check to report green despite running zero tests.
- **Shell loop patterns**: Explicitly verify that at least one iteration actually executed a non-skip command. Use a counter (e.g., `n=0; ... n=$((n+1)); done; [ "$n" -gt 0 ] || exit 1`) or similar guard. If the glob pattern or conditionals cause every iteration to be skipped (e.g., all matched files are excluded), the loop exits 0, falsely reporting health.
- **Type filtering with `find`**: Always use `find -type f` (not bare glob patterns or symlink-following globs) when discovering executable scripts or data files—a symlink named `*.sh` or `*.test.sh` pointing at arbitrary code should never be discovered and executed. Bare patterns like `*.test.sh` will follow symlinks; `-type f` restricts to regular files only (ticket #720).
- **Pre-execution file validation**: When discovering files to execute (especially via `find`), validate each file before invocation. Check that it is readable and non-empty (e.g., `[[ -r "$f" && -s "$f" ]]`) rather than relying solely on post-invocation error handling. This prevents false-greens from unreadable or zero-byte files, which would silently exit 0 when invoked as `bash <empty-file>` (mirroring the pattern used by `flow/skills/maintain/scripts/check.sh`'s `check_structural_tests`, ticket #720).

## This repo's gates (dogfooding)

This repo configures a `gateCommand` for each of its three projects in
`.cenci/config.json`:

| Project | `gateCommand` |
|---|---|
| `flow` | `bash scripts/run-checks.sh` |
| `watch` | `make test` |
| `sandbox` | `n=0; for t in tests/*.test.sh; do [ "$t" = tests/smoke.test.sh ] && continue; bash "$t" \|\| exit 1; n=$((n+1)); done; [ "$n" -gt 0 ] \|\| exit 1` |

`flow`'s gate delegates its JSON-validation and discovery/execution false-green
guards (the two bullets above) to `flow/scripts/run-checks.sh` — the same
script CI's `flow-test` job invokes, so the two never drift apart.

## See also

`flow/skills/configure/SKILL.md` — step 6, "Write `.cenci/config.json`",
documents the full config schema, including the per-project `gateCommand`
field (optional, unlike `lintCommand`, and not tied to the Stack-to-CI
mapping table).
