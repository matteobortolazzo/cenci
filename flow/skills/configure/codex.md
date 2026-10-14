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
`enrolled: false` asks the enroll question (unless a delta run finds `dispatchEnroll` in `configureOffered`) then the session question;
`enrolled: true, session: ""` asks only the session question; `enrolled: true, session: "<set>"` skips
both. The procedure never reads, modifies, or writes ~/.config/cenci/config.json itself
— every mutation goes through a single combined `cenci dispatch enroll --dir
'<main-checkout>' --session '<name>'` call, delegation-only, matching the Claude
procedure. Because the fleet dispatch enroll is a mutation and therefore runs only in the apply step
(`cenci run configure apply <checkpoint-id> --agent codex`), Plan mode
gathers the enroll/session answers but never calls `cenci dispatch enroll` itself —
never during `/plan`.

Delta re-runs mirror the Claude procedure's `### Delta Resolution` section. On a re-run
(an `existingConfig` exists, or a `migrationBase` merged from both `.cenci/config.json` and
`.claude/config.json`), run `bash
"${PLUGIN_ROOT}/skills/configure/scripts/resolve-delta.sh" <config-path>` as its own
command, and use its JSON line. A `migrationBase` is materialized to a slug-scoped scratch
file first, and that file is removed afterwards even when the resolver or a later overlay
fails. A legacy-only run (only `.claude/config.json`, no `.cenci/config.json`) is a full run
and never calls the resolver. Delta mode asks only the question IDs the resolver lists in `ask` and carries the rest,
so a carried answer skips only the prompt and its value still drives every generation
step. A leading `--all` in the user's request replays every question and ignores the ledger
(it never removes ledger entries); under `--all`, question 13 re-offers "Keep current (Recommended)" or a switch, and question 14 re-offers "Keep current block(s) (Recommended)" or "Remove the block(s)".
If the resolver fails, it falls back to a full replay and says why. A resolver result whose
`ask` or `carried` is not a JSON array fails the same check
(`jq -e '(.ask | type) == "array" and (.carried | type) == "array"'`) and falls back the same way.
A fallback full replay is not an `--all` run: it does not re-offer a present `planning` or
`automerge` value, so a fallback never puts the destructive "Remove the block(s)" option in front of the user.

Sandbox flags for `merge-sandbox-config.sh` are derived per question ID on a delta run. Questions
9, 9b and 9c (`sandbox.enabled`, `sandbox.dind`, `sandbox.azure`) listed in the resolver's `carried`
are never asked, so the flag for a carried ID is derived from the existing `sandbox.*` value
(`true` when it is `true`, `false` only for an absent key), never a bare `false` that would delete
an opt-in; an ID listed in `ask` (for example `sandbox.dind` and `sandbox.azure` on a config that
predates them) uses the answer collected this run, so a just-answered Yes is written and consumed.
Generate or refresh the managed `.cenci/Dockerfile` whenever the effective `sandbox.enabled` is
`true`, carried or just answered.

Every write stamps `configureOffered`: Codex stamps `configureOffered` with the same sorted, de-duplicated union as the Claude procedure,
recording only the questions actually put to the user and applied or explicitly declined. The question 13/14 overlays and
the ledger stamp use the exact programs the Claude procedure keeps between its
`cenci:configure-q13-switch`, `cenci:configure-q14-remove` and
`cenci:configure-offered-ledger` markers: Codex uses the same marker-bounded jq programs as `SKILL.md`,
and validates each overlay before the write: the `jq` call must exit 0 and the result must pass
`jq -e 'type == "object"'` (an empty output passes `jq empty`, so that alone is not a gate). When a question 13/14 overlay
falls back, its answer was not applied: the apply step tells the user (that the question will be asked
again on the next delta run only when its key is absent and its ID is not already in the existing ledger,
otherwise to re-run `/cenci:configure --all` to apply it), removes that question's ID
from the offered set before stamping the ledger, and reports only the overlay actually applied
(including the `## Review required — automerge policy removed` section). `dispatchEnroll` is recorded
only after a successful `cenci dispatch enroll` or an explicit "No"; an enroll failure is reported and not recorded.

On a delta run, Codex refreshes a valid marker-managed `.cenci/Dockerfile` without asking, and leaves a `.cenci/Dockerfile` with no markers or malformed markers untouched and reported;
`--all` restores the Overwrite/Skip/Show prompt. The Q12 dispatch session repair is still asked on a delta run. Board Config's
suggest-or-skip analyzer only reports on a delta run. Plan mode and the apply step split the work: the resolver runs read-only during `/plan`; the ledger, overlay and Dockerfile writes happen only in the apply step
(`cenci run configure apply <checkpoint-id> --agent codex`). The offered IDs and the collected
answers (the resolver's `ask` and `carried`, the effective sandbox flags, and the question 11/13/14 answers)
travel in the plan/apply checkpoint; the apply step reads them from there and never re-derives
them from the resolver, whose result may differ by then.

The apply step's report carries the same obligations as the Claude procedure's final report: the
carried-through summary (question IDs and config key names, never values in the PR body, and saying
`--all` would re-open them), the Dockerfile report (the block diff for a refreshed block, or "left untouched"
for no or malformed markers), the lazyboards report including the notice when question 10 is carried but
`.lazyboards.yml` is missing (it is not regenerated; `--all` re-offers it), and a ledger-not-stamped
notice when the ledger overlay fell back, so this run's questions will be asked again. A fallen-back
question 13/14 overlay or a failed `cenci dispatch enroll` is reported the same conditional way: asked
again on the next delta run only when the key is absent and the ID is not already in the existing ledger,
otherwise re-run `/cenci:configure --all`.

Autonomy Settings mirrors the Claude procedure's `### Autonomy Settings` section: the procedure on a delta run asks
about `planning` and `automerge` only when absent and not already offered — an already-present value (top-level or,
for `automerge`, any `projects[]` entry) is reported verbatim and never re-prompted, narrowed,
or removed on a delta run, and `--all` re-offers both with keep-or-change options. It defaults both offers to the conservative answer — `interactive` for planning,
no automerge block — and the automerge offer stays binary (skip vs. scaffold a starter block
for hand review), never a field-by-field sub-wizard. Because both keys land in the apply
step's config write, never during `/plan`, Plan mode only gathers the answers and previews the
scaffold; the actual `.cenci/config.json` write happens in
`cenci run configure apply <checkpoint-id> --agent codex`, same as every other field.
