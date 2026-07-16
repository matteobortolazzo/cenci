# Codex review procedure

Read `project-core`, `codex-runtime`, and `subagent-safety`. Gather the diff once, then use
the generated code/security reviewer adapters (or equivalent built-in workers) for bounded
parallel review. Validate every finding against the diff and report only actionable issues;
do not mutate code unless explicitly requested.
