# Codex configure procedure

Read `project-core` and `codex-runtime`. Enter `/plan`, inspect the repository, and gather
configuration choices with Plan-mode questions. Produce a migration preview using
`flow/scripts/migrate-project-core.sh`; do not apply it in Plan mode. After approval,
instruct the user to invoke `$cenci:configure apply <approved-plan>`. In normal mode,
write `.cenci/config.json`, AGENTS/CLAUDE adapters, `.claude/settings.json`,
`.codex/config.toml`, and `.codex/agents/*.toml`; preserve unknown keys and diff-gate
substantive guidance.
