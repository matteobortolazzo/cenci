# maintain — structure mode

Mode `structure` launches only the `structure-maintainer` agent (`Task` tool) in Phase 3 — Parallel audit. No other analyzer agent runs in this mode.

## Categories owned

- **Structure** — file/module organization, naming conventions, and structural drift relative to the conventions in `flow/AGENTS.md` and sibling skills.
- **Test gap** — missing or stale `flow/tests/*.test.sh` coverage. Sole-owned here: no other agent reports this category.

## Deterministic check first

Phase 2's `scripts/check.sh` run already covers several structural facts mechanically (`duplicate-names`, `broken-refs`, `orphan-files`, `structural-tests`). The agent's job in this mode is the judgment layer on top: findings the checker can't mechanically derive.

## Read-only

This mode's audit phase only reads the repository (`Read`/`Grep`/`Glob`/read-only `Bash`) and never mutates anything — mutation only happens in the shared `SKILL.md` Apply phase, after explicit approval.

## Approval

The approval options offered for this mode are the shared set defined in `SKILL.md`'s Phase 5 — Approval, scoped to the Structure and Test gap findings that actually ran, plus this invocation's Phase 2 deterministic-check results.
