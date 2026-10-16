# Codex refactor procedure

Before presenting proposals, apply the synthesis, security, and silent-failure
triggers in the plugin's `docs/model-escalation.md`. When triggered, delegate a
bounded second opinion to `domain-specialist`, supplying the completed findings
and source paths. Validate its evidence and preserve finding IDs, categories,
and approval scope; routine deduplication needs no specialist.

Read `project-core` and `codex-runtime`. Use read-heavy workers to analyze duplication,
structure, security, and silent failures. Produce ticket-sized proposals in `/plan`; create
GitHub tickets only after normal-mode approval. Do not combine unrelated refactors.
