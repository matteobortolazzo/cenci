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

The Azure CLI (`sandbox.azure`) is asked the same way: its own independent
Plan-mode question, ungated by the other two. There is no detection behind it
— an Azure repo can be written in any language — so default it to No on a
first-ever run and to the existing `existingConfig.sandbox.azure` value on
reconfiguration. When Yes, the generated `.cenci/Dockerfile` must include
`sandbox/fragments/azure.dockerfile`, and unlike dind there is no monolith
fallback: a Yes here with a No to the Dockerfile question leaves the repo with
no `az` at all, which the user should be told. When Yes, also tell the user
that `cenci open` stages their host `~/.azure` login read-only and seeds it
once into the sandbox's home volume.

Produce the `sandbox` field the same way Claude's SKILL.md does — by invoking
the shared, deterministic merge script rather than hand-merging the object,
so equivalent Dockerfile/dind/azure answers produce byte-equivalent `sandbox`
JSON for both clients:

```bash
bash "${PLUGIN_ROOT}/skills/configure/scripts/merge-sandbox-config.sh" \
  <path to existingConfig, or "-" piped from stdin when null> \
  --dockerfile <true|false, from the sandbox Dockerfile question> \
  --dind <true|false, from the nested Docker question> \
  --azure <true|false, from the Azure CLI question>
```

On success (exit 0), write its stdout as the new `.cenci/config.json` content
(after stamping `configVersion`). On any non-zero exit, do not use its
(possibly empty) stdout as the new config content — read stderr for the
cause. The script fails closed (exit 2) for several distinct reasons: `jq`
missing, an unreadable existing config, invalid existing JSON, a
missing/invalid `--dockerfile`/`--dind`/`--azure` value, or an
unknown argument. Every boolean flag is required — omitting one would default
it to false and silently delete an existing opt-in. If `jq` is genuinely
unavailable, fall back to the merge
outcomes documented in Claude's `SKILL.md` (same section, "sandbox" field)
and hand-construct the object from that table; for any other validation
failure, fix the inputs and retry the script rather than falling back. Never
construct the `sandbox` object by hand outside of that jq-unavailable
fallback.

Install missing native agents with `PLUGIN_ROOT=<plugin-root> sh
"${PLUGIN_ROOT}/codex/install-agents.sh" .`. Never overwrite an existing agent file;
show a diff and ask before updating one. Validate each installed TOML against the
agent-role schema with `bash "${PLUGIN_ROOT}/codex/validate-agent-roles.sh"
.codex/agents`, in addition to the existing read-only `--strict-config` Codex smoke
check.

Fleet dispatch enrollment mirrors the Claude procedure's `### Fleet Dispatch Enrollment`
section exactly. Container guard: In-container, the section asks nothing, runs nothing, and writes nothing
— the sandbox's `~/.config/cenci/config.json` is a
container-local volume, not the host file the dispatch daemon reads — Plan mode prints
the host-side `cenci dispatch enroll --session <name>` fix instead. Main-checkout
resolution uses the same `git rev-parse --path-format=absolute --git-common-dir` call
(logging one informational line before falling back to the Scripted Detection root on
failure), and the
same three-way branch runs on `cenci dispatch status --json --dir '<main-checkout>'`:
`enrolled: false` asks the enroll question then the session question;
`enrolled: true, session: ""` asks only the session question; `enrolled: true, session: "<set>"` skips
both. The procedure never reads, modifies, or writes ~/.config/cenci/config.json itself
— every mutation goes through a single combined `cenci dispatch enroll --dir
'<main-checkout>' --session '<name>'` call, delegation-only, matching the Claude
procedure. Because the fleet dispatch enroll is a mutation and therefore runs only in the apply step
(`cenci run configure apply <checkpoint-id> --agent codex`), Plan mode
gathers the enroll/session answers but never calls `cenci dispatch enroll` itself —
never during `/plan`.

Autonomy Settings mirrors the Claude procedure's `### Autonomy Settings` section: it asks
about `planning` and `automerge` only when absent — an already-present value (top-level or,
for `automerge`, any `projects[]` entry) is reported verbatim and never re-prompted, narrowed,
or removed. It defaults both offers to the conservative answer — `interactive` for planning,
no automerge block — and the automerge offer stays binary (skip vs. scaffold a starter block
for hand review), never a field-by-field sub-wizard. Because both keys land in the apply
step's config write, never during `/plan`, Plan mode only gathers the answers and previews the
scaffold; the actual `.cenci/config.json` write happens in
`cenci run configure apply <checkpoint-id> --agent codex`, same as every other field.
