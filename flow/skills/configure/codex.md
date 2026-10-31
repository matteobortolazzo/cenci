# Codex configure procedure

Planning-request Plan-mode self-check first: before any other planning step, follow `codex-runtime`'s **Approved-plan handoff** (artifact `.plans/codex/configure-repo.md`, target `repo`).

Read `project-core` and `codex-runtime`. In Plan mode, inspect the repository and gather
configuration choices with Plan-mode questions. Produce a migration preview using
`"${PLUGIN_ROOT}/scripts/migrate-project-core.sh"`; do not apply it in Plan mode. After approval,
the persist step records the preview, the answers and the offered IDs in the approved-plan artifact,
and the user continues with `cenci run configure apply repo --agent codex`. In apply,
write `.cenci/config.json`, AGENTS/CLAUDE adapters, `.claude/settings.json`,
and `.codex/agents/*.toml`; preserve unknown keys and diff-gate substantive guidance.

The `docs` config field mirrors the `docs` schema paragraph in `references/config-schema.md` and, like `maintenance`, has no question.
Preserve the optional implementation controls of the `cenci` schema in `references/config-schema.md` on reconfiguration:
`cenci.compactImplementation` defaults to false (repository opt-in only),
`cenci.implementerConcurrency` defaults to `"parallel"`, and
`cenci.implementerMaxConcurrency` defaults to 2 and accepts positive integers.
The implement procedure applies that cap within dependency waves, further limited by
available worker slots; sequential mode still delegates each lane and keeps all gates.
Do not overwrite existing values with scaffold defaults or add a configure question.
`docs.policy` is an optional field that no configure prompt writes, and a configure re-run preserves it together with every unrelated key.
Plan-required and correctness documentation work always runs, while incidental documentation edits and lesson capture are what `minimal` suppresses.
Only the exact string `"minimal"` selects that policy; anything else, or no `docs` block, is the default. Do **not** add `docs` to the migration-removal list — it is a supported optional field, not a legacy one.

Deterministic detection is scripted, same as the Claude procedure: run `bash
"${PLUGIN_ROOT}/skills/configure/scripts/detect-project.sh" --plugin-root
"${PLUGIN_ROOT}"` from the repo root and consume its JSON (platform, container,
package manager, MCP/LSP/dind/Playwright triggers, plugin version) instead of
re-deriving them; fall back to the manual detection in the Claude procedure's phase
files (`phases/detection-and-delta.md` for platform and container,
`phases/mcp-lsp-ui-questions.md` for dependencies, LSP and dind,
`phases/runtime-ci-sandbox-questions.md` for the package manager) only when the
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

Produce the `sandbox` field the same way step 6 in `phases/write-config.md` does — by invoking
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
outcomes documented in `phases/write-config.md` (its `sandbox` merge section)
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
section in `phases/fleet-enrollment.md` exactly. Container guard: In-container, the section asks nothing, runs nothing, and writes nothing
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
(`cenci run configure apply repo --agent codex`), Plan mode
gathers the enroll/session answers but never calls `cenci dispatch enroll` itself —
never during `/plan`.

Delta re-runs mirror the Claude procedure's `### Delta Resolution` section (in
`phases/detection-and-delta.md`). On a re-run
(an `existingConfig` exists, or a `migrationBase` merged from both `.cenci/config.json` and
`.claude/config.json`), run `bash
"${PLUGIN_ROOT}/skills/configure/scripts/resolve-delta.sh" <config-path>` as its own
command, and use its JSON line. A `migrationBase` is materialized to a slug-scoped scratch
file first, and that file is removed afterwards even when the resolver or a later overlay
fails. A legacy-only run (only `.claude/config.json`, no `.cenci/config.json`) is a full run
and never calls the resolver. Delta mode asks only the question IDs the resolver lists in `ask` and carries the rest,
so a carried answer skips only the prompt and its value still drives every generation
step. A leading `--all` in the user's request replays every question and ignores the ledger
(it never removes ledger entries); under `--all`, question 13 re-offers "Keep current (Recommended)" or a switch, question 14 re-offers "Keep current block(s) (Recommended)" or "Remove the block(s)", and question 15 re-offers "Keep current (Recommended)" or a switch.
If the resolver fails, it falls back to a full replay and says why. A resolver result whose
`ask` or `carried` is not a JSON array fails the same check
(`jq -e '(.ask | type) == "array" and (.carried | type) == "array"'`) and falls back the same way.
A fallback full replay is not an `--all` run: it does not re-offer a present `planning`,
`automerge` or `refinement` value, so a fallback never puts the destructive "Remove the block(s)" option in front of the user.

Sandbox flags for `merge-sandbox-config.sh` are derived per question ID on a delta run. Questions
9, 9b and 9c (`sandbox.enabled`, `sandbox.dind`, `sandbox.azure`) listed in the resolver's `carried`
are never asked, so the flag for a carried ID is derived from the existing `sandbox.*` value
(`true` when it is `true`, `false` only for an absent key), never a bare `false` that would delete
an opt-in; an ID listed in `ask` (for example `sandbox.dind` and `sandbox.azure` on a config that
predates them) uses the answer collected this run, so a just-answered Yes is written and consumed.
Generate or refresh the managed `.cenci/Dockerfile` whenever the effective `sandbox.enabled` is
`true`, carried or just answered.

Every write stamps `configureOffered`: Codex stamps `configureOffered` with the same sorted, de-duplicated union as the Claude procedure,
recording only the questions actually put to the user and applied or explicitly declined. The question 13/14/15 overlays and
the ledger stamp use the exact programs the Claude procedure keeps between its
`cenci:configure-q13-switch`, `cenci:configure-q14-remove`, `cenci:configure-q15-switch` and
`cenci:configure-offered-ledger` markers: Codex uses the same marker-bounded jq programs as `phases/autonomy-settings.md`
(the `cenci:configure-offered-ledger` program from step 6 in `phases/write-config.md`),
and validates each overlay before the write: the `jq` call must exit 0 and the result must pass
`jq -e 'type == "object"'` (an empty output passes `jq empty`, so that alone is not a gate). When a question 13/14/15 overlay
falls back, its answer was not applied: the apply step tells the user (that the question will be asked
again on the next delta run only when its key is absent and its ID is not already in the existing ledger,
otherwise to re-run `/cenci:configure --all` to apply it), removes that question's ID
from the offered set before stamping the ledger, and reports only the overlay actually applied
(including the `## Review required — automerge policy removed` section). `dispatchEnroll` is recorded
only after a successful `cenci dispatch enroll` or an explicit "No"; an enroll failure is reported and not recorded.

On a delta run, Codex refreshes a valid marker-managed `.cenci/Dockerfile` without asking, and leaves a `.cenci/Dockerfile` with no markers or malformed markers untouched and reported;
`--all` restores the Overwrite/Skip/Show prompt. The Q12 dispatch session repair is still asked on a delta run. Board Config's
suggest-or-skip analyzer only reports on a delta run. Plan mode and the apply step split the work: the resolver runs read-only during `/plan`; the ledger, overlay and Dockerfile writes happen only in the apply step
(`cenci run configure apply repo --agent codex`). The offered IDs and the collected
answers (the resolver's `ask` and `carried`, the effective sandbox flags, and the question 11/13/14/15/16/17 answers and the independent fleet 18–20 choices)
travel in the approved-plan artifact; the apply step reads them from there and never re-derives
them from the resolver, whose result may differ by then.

The apply step's report carries the same obligations as the final report in `phases/commit-pr.md`: the
carried-through summary (question IDs and config key names, never values in the PR body, and saying
`--all` would re-open them), the Dockerfile report (the block diff for a refreshed block, or "left untouched"
for no or malformed markers), the lazyboards report including the notice when question 10 is carried but
`.lazyboards.yml` is missing (it is not regenerated; `--all` re-offers it), and a ledger-not-stamped
notice when the ledger overlay fell back, so this run's questions will be asked again. A fallen-back
question 13/14/15 overlay or a failed `cenci dispatch enroll` is reported the same conditional way: asked
again on the next delta run only when the key is absent and the ID is not already in the existing ledger,
otherwise re-run `/cenci:configure --all`.

Autonomy Settings mirrors the Claude procedure's `### Autonomy Settings` section in `phases/autonomy-settings.md`: the procedure on a delta run asks
about `planning`, `automerge` and `refinement` only when absent and not already offered — an already-present value (top-level or,
for `automerge`, any `projects[]` entry) is reported verbatim and never re-prompted, narrowed,
or removed on a delta run, and `--all` re-offers all three with keep-or-change options. It defaults the planning and automerge offers to the conservative answer — `interactive` for planning,
no automerge block — while question 15 recommends `lean` and an absent `refinement` key still resolves to `interactive`. The automerge offer stays binary (skip vs. scaffold a starter block
for hand review), never a field-by-field sub-wizard. Question 15 (`refinement`) mirrors question 13 with the options "Switch to `lean` (Recommended)" and "Keep `interactive`"; under `--all` with `refinement` present it offers "Keep current (Recommended)" or the switch to the other value, and any value other than the exact string `lean` is treated as `interactive`. Declining question 15 writes no `refinement` key. Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently. Because all three keys land in the apply
step's config write, never during `/plan`, Plan mode only gathers the answers and previews the
scaffold; the actual `.cenci/config.json` write happens in
`cenci run configure apply repo --agent codex`, same as every other field.

Independent controls mirror questions 16–17 in `phases/autonomy-settings.md` and 18–20 in `phases/fleet-enrollment.md` using the client's
available Plan-mode question mechanism. `refinement.confirmBeforeApply` defaults to
false: offer apply immediately (Recommended) or require confirmation. Record the
boolean in `confirmBeforeApplyAnswer`, preserving an explicit false. Invalid values
resolve to true with a notice. `refinement.automerge` defaults to off: offer off
(Recommended) or eligible. Record `refinementAutomergeAnswer`; malformed values
resolve to off with a notice. `eligible` grants only tickets passing refine's
eligibility checks, independently of `planning.autonomy` and `refinement.autonomy`.
Question selection never itself grants merging or changes confirmation.

Both leaves have independent stable question IDs in `question-keys.json`: an existing
`refinement` object or old `refinement` ledger entry never suppresses either leaf.
Delta runs offer absent/unoffered leaves; carried leaves are preserved, and `--all`
re-offers each with its current value first. Resolver-failure full runs carry present
leaves. Carry both collected answers and offered IDs in the approved-plan artifact.
After the Q13/14/15 overlays and before the ledger overlay, apply the exact
`cenci:configure-confirm-before-apply` and `cenci:configure-refinement-automerge`
marker-bounded jq programs from `phases/autonomy-settings.md`, with `--argjson confirmBeforeApply
<true|false>` and `--arg automerge <off|eligible>` respectively. Validate answer types
and allowed values first, then check exit 0 and the object gate after each overlay.
Apply based on answer presence, never truthiness or a jq `//` fallback that loses
false. Failed leaf overlays keep the previous candidate and remove only that leaf's
ID from the offered set; report the answer as not applied. The programs preserve
all siblings; off never removes the separate top-level/per-project risk policy.

Independently offer the keyless fleet question IDs `dispatch.planRefined`,
`automerge.enabled`, and `planning.attended` (keep current/On/Off). They never derive
answers from another control or write fleet fields into repo config. Use the existing
Fleet Dispatch Enrollment container guard. On the host, probe their states separately
with `cenci dispatch plan-refined status`, `cenci automerge status`, and
`cenci planning attended status --json`. Probe failure leaves state unknown and still
allows an explicit choice. Apply each on/off answer through its matching CLI command
only in the apply step: `cenci dispatch plan-refined on|off`, `cenci automerge on|off`,
`cenci planning attended on|off`. Never read/write the host config file directly.
Only successful CLI calls or explicit keep choices stamp their own ledger IDs;
on failure the setting may already have persisted before a later status query failed.
Report an unknown or potentially partially applied outcome, never unchanged, and add
no ID. Re-probe through the matching status command when possible and report only
what it verifies. In a sandbox, collect the desired
answers but run no fleet status or mutation calls; print concrete host on/off commands
for explicit choices and never claim deferred fleet choices were applied. Deferred
choices are not stamped. Report all independent choices' actual outcomes after apply.
