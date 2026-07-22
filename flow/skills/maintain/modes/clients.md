# maintain — clients mode

Mode `clients` launches only the `portability-maintainer` agent (`Task` tool) in Phase 3 — Parallel audit. No other analyzer agent runs in this mode.

Note the intentional mode-name/agent-name divergence: the mode is named `clients` because it is user-facing ("which AI clients does this support"), while the agent is named `portability-maintainer` because it matches the existing "portability" vocabulary already used in `flow/docs/codex.md` and `flow/docs/opencode.md`. Do not rename either to force a literal match.

## Category owned

- **Client mismatch** — disagreement between what a skill actually supports and what `flow/README.md`'s hand-curated "Skill portability" table, the generated skill inventory, `flow/opencode/install-skills.sh`'s `PORTABLE_SKILLS`, or a skill's own companion files claim.

## Deterministic check first

Phase 2's `scripts/check.sh` run already covers several client-portability facts mechanically (`capability-table`, `adapter-drift`). The agent's job in this mode is the judgment layer on top: findings the checker can't mechanically derive.

## Read-only

This mode's audit phase only reads the repository (`Read`/`Grep`/`Glob`/read-only `Bash`) and never mutates anything — mutation only happens in the shared `SKILL.md` Apply phase, after explicit approval.

## Approval

The approval options offered for this mode are the shared set defined in `SKILL.md`'s Phase 5 — Approval, scoped to the Client mismatch findings that actually ran, plus this invocation's Phase 2 deterministic-check results.
