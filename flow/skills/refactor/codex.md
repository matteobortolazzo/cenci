# Codex refactor procedure

Before presenting proposals, apply the synthesis, security, and silent-failure
triggers in the plugin's `docs/model-escalation.md`. When triggered, delegate a
bounded second opinion to `domain-specialist`, supplying the completed findings
and source paths. Validate its evidence and preserve finding IDs, categories,
and approval scope; routine deduplication needs no specialist.

Read `project-core` and `codex-runtime`. Use read-heavy workers to analyze duplication,
structure, security, and silent failures. Produce ticket-sized proposals in `/plan`.
In normal mode, before any mutation, run the guard canary preflight,
`bash "${PLUGIN_ROOT}/codex/guard-canary.sh" plan`, as `codex-runtime` describes; on any refusal stop with nothing
created, reporting the helper's text and any `LEFTOVER:` path. Only after `CANARY_STATUS=active`,
create GitHub tickets, and only after normal-mode approval. Do not combine unrelated refactors.
