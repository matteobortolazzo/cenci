# Codex configure procedure

Read `project-core` and `codex-runtime`. Enter `/plan`, inspect the repository, and gather
configuration choices with Plan-mode questions. Produce a migration preview using
`flow/scripts/migrate-project-core.sh`; do not apply it in Plan mode. After approval,
instruct the user to invoke `cenci run configure apply <checkpoint-id> --agent codex`;
the approved plan remains in the prior conversation and the checkpoint records its digest. In normal mode,
write `.cenci/config.json`, AGENTS/CLAUDE adapters, `.claude/settings.json`,
and `.codex/agents/*.toml`; preserve unknown keys and diff-gate substantive guidance.

Deterministic detection is scripted, same as the Claude procedure: run `bash
"${PLUGIN_ROOT}/skills/configure/scripts/detect-project.sh" --plugin-root
"${PLUGIN_ROOT}"` from the repo root and consume its JSON (platform, container,
package manager, MCP/LSP/dind/Playwright triggers, plugin version) instead of
re-deriving them; fall back to the manual detection in the skill body only when the
script cannot run. When writing `.cenci/config.json`, always stamp `configVersion`
with the resolved plugin version — the staleness advisories (Claude's SessionStart
hook and maintain's `config-version` check) rely on it to tell users when a
configure re-run would pick up new features.

Install missing native agents with `PLUGIN_ROOT=<plugin-root> sh
"${PLUGIN_ROOT}/codex/install-agents.sh" .`. Never overwrite an existing agent file;
show a diff and ask before updating one. Validate each installed TOML by starting Codex
with `--strict-config` in a read-only smoke check.
