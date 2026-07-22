# maintain — docs mode

Mode `docs` launches only the `docs-maintainer` agent (`Task` tool) in Phase 3 — Parallel audit. No other analyzer agent runs in this mode.

## Categories owned

- **Documentation drift** — stale or misleading prose in `flow/AGENTS.md`, `flow/docs/*.md`, or `flow/README.md`'s hand-curated sections relative to the actual behavior they describe.
- **Generated index drift** — marker-bounded sections in `flow/README.md` (skills, agents, workflow-deps, docs-nav) that are out of date relative to their canonical sources.

## Deterministic check first

Phase 2's `scripts/check.sh` run already covers several documentation facts mechanically (`instruction-docs`, `topic-docs`, `stale-generated`, `command-flags`, `config-examples`, `roadmap-status`). The agent's job in this mode is the judgment layer on top: findings the checker can't mechanically derive.

## Read-only

This mode's audit phase only reads the repository (`Read`/`Grep`/`Glob`/read-only `Bash`) and never mutates anything — mutation only happens in the shared `SKILL.md` Apply phase, after explicit approval.

## Approval

The approval options offered for this mode are the shared set defined in `SKILL.md`'s Phase 5 — Approval, scoped to the Documentation drift and Generated index drift findings that actually ran, plus this invocation's Phase 2 deterministic-check results. This is the only mode where the "docs+indexes only" option applies.
