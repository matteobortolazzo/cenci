# Codex configure procedure

Read `project-core` and `codex-runtime`. Enter `/plan`, inspect the repository, and gather
configuration choices with Plan-mode questions. Produce a migration preview using
`"${PLUGIN_ROOT}/scripts/migrate-project-core.sh"`; do not apply it in Plan mode. After approval,
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

Nested Docker (dind) is asked as its own independent Plan-mode question,
regardless of how the per-repo sandbox Dockerfile question was answered —
enabling or declining the Dockerfile never gates it. Default it to Yes when
`detection.dindDetected` is true (else No) on a first-ever run; on
reconfiguration, default to the existing `existingConfig.sandbox.dind` value
instead, so re-running configure never silently flips an already-made choice.
Produce the `sandbox` field the same way Claude's SKILL.md does — by invoking
the shared, deterministic merge script rather than hand-merging the object,
so equivalent Dockerfile/dind answers produce byte-equivalent `sandbox` JSON
for both clients:

```bash
bash "${PLUGIN_ROOT}/skills/configure/scripts/merge-sandbox-config.sh" \
  <path to existingConfig, or "-" piped from stdin when null> \
  --dockerfile <true|false, from the sandbox Dockerfile question> \
  --base-version <resolved baseVersion, or the literal "null"> \
  --dind <true|false, from the nested Docker question>
```

On success (exit 0), write its stdout as the new `.cenci/config.json` content
(after stamping `configVersion`). On any non-zero exit, do not use its
(possibly empty) stdout as the new config content — read stderr for the
cause. The script fails closed (exit 2) for several distinct reasons: `jq`
missing, an unreadable existing config, invalid existing JSON, a
missing/invalid `--dockerfile`/`--dind`/`--base-version` value, or an unknown
argument. If `jq` is genuinely unavailable, fall back to the four merge
outcomes documented in Claude's `SKILL.md` (same section, "sandbox" field)
and hand-construct the object from that table; for any other validation
failure, fix the inputs and retry the script rather than falling back. Never
construct the `sandbox` object by hand outside of that jq-unavailable
fallback.

Install missing native agents with `PLUGIN_ROOT=<plugin-root> sh
"${PLUGIN_ROOT}/codex/install-agents.sh" .`. Never overwrite an existing agent file;
show a diff and ask before updating one. Validate each installed TOML by starting Codex
with `--strict-config` in a read-only smoke check.
