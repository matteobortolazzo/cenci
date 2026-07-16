# Codex configure procedure

Read `project-core` and `codex-runtime`. Enter `/plan`, inspect the repository, and gather
configuration choices with Plan-mode questions. Produce a migration preview using
`flow/scripts/migrate-project-core.sh`; do not apply it in Plan mode. After approval,
instruct the user to invoke `cenci run configure apply <checkpoint-id> --agent codex`;
the approved plan remains in the prior conversation and the checkpoint records its digest. In normal mode,
write `.cenci/config.json`, AGENTS/CLAUDE adapters, `.claude/settings.json`,
`.codex/config.toml`, and `.codex/agents/*.toml`; preserve unknown keys and diff-gate
substantive guidance.

Install missing native agents with `PLUGIN_ROOT=<plugin-root> sh
"${PLUGIN_ROOT}/codex/install-agents.sh" .`. Never overwrite an existing agent file;
show a diff and ask before updating one. Validate each installed TOML by starting Codex
with `--strict-config` in a read-only smoke check.
