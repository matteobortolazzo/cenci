# Codex refactor procedure

Planning-request Plan-mode self-check first: before any other planning step, follow `codex-runtime`'s **Approved-plan handoff** (artifact `.plans/codex/refactor-repo.md`, target `repo`).

Before presenting proposals, apply the synthesis, security, and silent-failure
triggers in the plugin's `docs/model-escalation.md`. When triggered, delegate a
bounded second opinion to `domain-specialist`, supplying the completed findings
and source paths. Validate its evidence and preserve finding IDs, categories,
and approval scope; routine deduplication needs no specialist.

Read `project-core` and `codex-runtime`. Use read-heavy workers to analyze duplication,
structure, security, and silent failures. Produce ticket-sized proposals in Plan mode; create
GitHub tickets only in `apply`, after the persist step recorded the approved proposals in the artifact. Do not combine unrelated refactors.
