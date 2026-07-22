# maintain — rules mode

Mode `rules` launches only the `rules-maintainer` agent (`Task` tool) in Phase 3 — Parallel audit. No other analyzer agent runs in this mode.

## Category owned

- **Rule hygiene** — curation of `CLAUDE.md`/`AGENTS.md` `## Critical Rules` bullets, topic-doc (`docs/*.md`) rule bullets, and legacy `lessons-learned*.md` files. Sole-owned here: no other agent reports this category.

## Deterministic check first

Phase 2's `scripts/check.sh` run (`context-budget`) is the source of truth for the context-budget thresholds that suggest a maintain-rules pass — a `## Critical Rules` section or a `docs/<topic>.md` growing past its mark. This file does not restate those numbers; see `scripts/check.sh`'s implementation for the current marks. The agent's job in this mode is the judgment layer on top: classifying every in-scope rule, which the checker can't do on its own.

## Phase 3 contribution: Inventory + Audit

This mode's contribution to the shared Parallel audit phase is the retired garden skill's own Phase 1 (Inventory) and Phase 2 (Audit), run by `rules-maintainer` instead of as a standalone skill's phases:

- **Inventory** — enumerate `## Critical Rules` bullets in `CLAUDE.md`/`AGENTS.md`, rule bullets
  in `docs/*.md`, and any legacy `lessons-learned*.md` files.
- **Audit** — classify every in-scope rule into Keep, Tighten, Merge, Relocate, Demote, or
  Archive, per `rules-maintainer`'s evidence discipline (fresh `Grep`/`Read` evidence for
  Demote/Archive, quoted bullets for Merge, default Keep).

## Legacy migration

Surviving entries in any legacy `lessons-learned*.md` file are proposed as moves to their proper homes (`docs/<topic>.md` or Critical Rules) plus deletion of the legacy file, applied in the same PR during the shared Phase 6 — Apply. No separate worktree, commit, or PR flow is duplicated for this mode.

## Read-only

This mode's audit phase only reads the repository (`Read`/`Grep`/`Glob`/read-only `Bash`) and never mutates anything — mutation only happens in the shared `SKILL.md` Apply phase, after explicit approval.

## Approval

The approval options offered for this mode are the shared set defined in `SKILL.md`'s Phase 5 — Approval, scoped to the Rule hygiene findings that actually ran, plus this invocation's Phase 2 deterministic-check results. This is the only mode where the "rules only" option applies.
