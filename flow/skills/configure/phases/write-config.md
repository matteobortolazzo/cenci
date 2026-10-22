# Write `.cenci/config.json`

Read this file on every run, after `phases/ci-dockerfile.md` (see `SKILL.md` `### Step Map`). It holds step 6, the `.cenci/config.json` write: merge semantics, autonomy and leaf wiring, overlay order and validation, the `configVersion` stamp and the `configureOffered` ledger, followed by the `sandbox` merge section. Step 6 reads `references/config-schema.md` for the example and field-by-field schema of the file it writes.

6. **Write `.cenci/config.json`** with their choices using **merge semantics**:

   - If `existingConfig` is not null: start from the existing object, overwrite each field with the user's answers. This preserves fields the skill doesn't manage.
   - If `existingConfig` is null: create the file fresh.

   **Autonomy Settings wiring.** `planningAnswer`/`automergeAnswer`/`refinementAnswer` are overlaid onto
   `merge-sandbox-config.sh`'s stdout before the write: `planningAnswer` contributes a
   `planning: { "autonomy": "lean" }` key only when question 13 in `phases/autonomy-settings.md` was asked and
   answered `lean`, `automergeAnswer` contributes the scaffolded `automerge` key only
   when question 14 in `phases/autonomy-settings.md` was asked and answered "Scaffold a starter block for hand
   review", and `refinementAnswer` contributes a `refinement: { "autonomy": "lean" }` key
   only when question 15 in `phases/autonomy-settings.md` was asked and answered `lean` — a declined answer contributes no key. When any of the three questions was skipped
   because its key was already present in `existingConfig` (or carried on a delta run),
   there is no collected answer to overlay — the existing value simply survives the merge
   semantics above untouched. Under `--all`, a collected "Switch to …" answer for question
   13 is applied with the `cenci:configure-q13-switch` program (`--arg autonomy
   interactive|lean`), a collected "Switch to …" answer for question 15 is applied with the `cenci:configure-q15-switch` program (`--arg autonomy interactive|lean`), and a collected "Remove the block(s)" answer for question 14 with
   the `cenci:configure-q14-remove` program (`### Autonomy Settings` in `phases/autonomy-settings.md`); "Keep current"
   answers contribute nothing.

   **Independent refinement leaves wiring.** After the question 13/14/15 overlays,
   apply `confirmBeforeApplyAnswer` with `cenci:configure-confirm-before-apply`
   using `--argjson confirmBeforeApply <true|false>`, and apply
   `refinementAutomergeAnswer` with `cenci:configure-refinement-automerge` using
   `--arg automerge <off|eligible>`. Validate the answer types and allowed values
   before invoking jq. Presence of a collected answer, including `false`, controls
   whether its overlay runs; never use truthiness or `//` to discard false. A skipped
   leaf has no overlay and survives unchanged. Both overlays preserve siblings, so
   the Q15 autonomy overlay and these leaves can run in one write. Apply the same
   object validation and fallback as question 13/14/15 in `phases/autonomy-settings.md`; a failed leaf overlay
   removes only its own ID from this run's offered set before the ledger stamp and
   is reported as not applied. Fleet answers affect only CLI calls and their ledger
   IDs; never insert `dispatch.planRefined`, `automerge.enabled`, or `planning.attended`
   values into this repo's configuration. A deferred or failed fleet call contributes
   no ledger ID. Each actually applied or explicitly kept repo leaf is recorded
   under its independent question ID, even when its explicit value is false or off.

   **Overlay order and validation.** The overlay order is `merge-sandbox-config.sh` stdout, then the question 13/14/15 overlays (`cenci:configure-q13-switch`, `cenci:configure-q14-remove`, `cenci:configure-q15-switch`), then the independent leaf overlays (`cenci:configure-confirm-before-apply`, `cenci:configure-refinement-automerge`), then the `configureOffered` ledger (`cenci:configure-offered-ledger`). Run each overlay with `jq` over the candidate config as its own Bash call (write the candidate to `${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-config.json` as the scratch input and remove it afterwards under the existing `rm -f` grant — that `rm -f` also runs when an overlay fails validation, so no scratch file is left behind). An overlay result is valid only when the `jq` call exits 0 and the result passes `jq -e 'type == "object"'`: an empty output passes `jq empty`, so `jq empty` alone never gates a write. Apply that check after each overlay and before every write — the same fail-closed discipline as the adjacent `sandbox` merge above: if an overlaid result fails it, do not write it, report the failure, and fall back to the content before that overlay (the previous step's output as-is). A fallback that drops the ledger overlay still writes, but tell the user the ledger could not be stamped, so the questions offered this run will be asked again.

   **A fallen-back question 13/14/15 overlay was not applied.** When a question 13, 14 or 15 overlay fails the check above, the collected answer is not in the written config. Tell the user which question's answer was not applied, and either that it will be asked again on the next delta run (only when its key is absent and its ID is not already in the existing ledger) or, otherwise (an `--all` change, an ID already in the existing ledger, or a present key), to re-run `/cenci:configure --all` to apply it; remove that question's ID (`planning` for question 13, `automerge` for question 14, `refinement` for question 15) from the offered set before the ledger overlay runs, so the ledger never records an offer whose answer was dropped; and key step 7 to the overlay actually applied — a fallen-back "Remove the block(s)" never emits the `## Review required — automerge policy removed` section and is never reported as removed, and a fallen-back scaffold never emits `## Review required — automerge policy`.

   **Sandbox flags are derived per question ID.** For an ID in `delta.carried`, the Q9/Q9b/Q9c flag is derived from the existing `sandbox.*` value, never passed as a bare `false`: pass `true` for each of `--dockerfile`, `--dind` and `--azure` whose existing `sandbox.enabled`/`dind`/`azure` is `true`, and `false` only for a key that is absent (a recorded decline) — so a carried opt-in is never deleted by the merge script's required flags. For an ID in `delta.ask` (a key that was absent and never offered, e.g. `sandbox.dind` and `sandbox.azure` on a config that predates questions 9b/9c), and for every question on an `--all` or `full` run, the flag is the answer collected this run. Generate or refresh the `.cenci/Dockerfile` (step 5e) whenever the effective `sandbox.enabled` is `true`, carried or just answered; a just-answered Yes for `--dind` or `--azure` selects its fragment in that same generation.

   **Always stamp `configVersion`** on every write (fresh or re-config): set it to `detection.pluginVersion`. If Scripted Detection was unavailable, read `.version` from `${CLAUDE_PLUGIN_ROOT}/.claude-plugin/plugin.json` with jq, verifying the command succeeded before using its output. If neither resolves, preserve any existing `configVersion` unchanged and tell the user the stamp could not be refreshed — never invent a version.

Always stamp `configureOffered` on every write as the sorted, de-duplicated union of the existing ledger and the question IDs offered this run; `--all` never removes ledger entries. Only questions actually put to the user this run are recorded as offered (the IDs from the `ID` column of the defaults table): a carried question, a conditional question whose detection condition failed, and question 12 are never recorded, and entries are never pruned. String entries of an existing `configureOffered` array are kept and non-strings are dropped. An empty result is still written as `[]`. Apply this fixed program last in the overlay order, with `--argjson offered '[<ids>]'` (the JSON array of offered IDs, `[]` when no question was asked — minus any ID removed above because its overlay fell back, or because question 11's enroll failed), then validate it like every other overlay (exit 0 and `jq -e 'type == "object"'`):

<!-- cenci:configure-offered-ledger:begin -->
```jq
.configureOffered = (
  ((.configureOffered // []) | if type == "array" then map(select(type == "string")) else [] end)
  + ($offered | map(select(type == "string")))
  | unique
)
```
<!-- cenci:configure-offered-ledger:end -->

### Sandbox merge

The `sandbox` field carries three **independently toggled** sub-answers — question 9 (Sandbox Dockerfile), question 9b (Nested Docker/dind) and question 9c (Azure CLI) — that do not gate each other. Its merge into `existingConfig` (or a fresh config) is delegated to a shared, deterministic script rather than hand-merged, so Claude Code and Codex configure runs produce byte-equivalent `sandbox` JSON for equivalent answers — "prose presence alone is not evidence that generated JSON is correct" (#632):

```bash
bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/merge-sandbox-config.sh" <path to existingConfig, or "-" for stdin> \
  --dockerfile <true|false, from question 9> \
  --dind <true|false, from question 9b> \
  --azure <true|false, from question 9c>
```

Run it as its own Bash call (per `cenci:shell-rules`). If `existingConfig` is null (first-ever configure run), pass `-` as the config argument and pipe `{}` in as stdin. The script prints the **full merged config** (not just the `sandbox` object) to stdout — treat that stdout as the new full config content going into step 6's write (after also stamping `configVersion` per above).

`scripts/merge-sandbox-config.sh` (tested by its own `scripts/merge-sandbox-config.test.sh`) is the source of truth for the merge; the schema and outcomes below document its contract, not a procedure to hand-execute. On any non-zero exit from the script, do not use its (possibly empty) stdout as the new config content — read stderr for the cause. The script fails closed (exit 2) for several distinct reasons: `jq` missing, an unreadable existing config, invalid existing JSON, a missing/invalid `--dockerfile`/`--dind`/`--azure` value, or an unknown argument. Every boolean flag is **required** — an omitted one would default to false and silently delete an existing opt-in, so the script refuses instead. If `jq` is genuinely unavailable, fall back to this manual procedure; for any other validation failure, fix the inputs (e.g. re-check the existing config's readability/JSON validity, the resolved flag values) and retry the script rather than falling back:
- `sandbox.enabled` — `true` if the user opted in to question 9; omit the key entirely if declined (same pattern as `cicd` — never write `enabled: false`). The script also deletes a legacy `sandbox.baseVersion` it finds on the existing config — that key is retired (see step 5e in `phases/ci-dockerfile.md`).
- `sandbox.dind` — `true` if the user opted in to question 9b; omit the key entirely if declined (never write `dind: false`)
- `sandbox.azure` — `true` if the user opted in to question 9c; omit the key entirely if declined (never write `azure: false`). Read by `/cenci:configure` (to select `sandbox/fragments/azure.dockerfile`) **and** at launch by `cenci open`/`cenci audit` (`RepoAzureConfig` in `watch/internal/sandbox/launcher/azure.go`), which stage the host's `~/.azure` auth files read-only only for repos that set it

Because the answers are independent, `sandbox` is written whenever **any** of them is Yes:
- Q9=Yes, 9b=No, 9c=No → `"sandbox": { "enabled": true }` (no `dind`/`azure` keys)
- Q9=No, 9b=Yes, 9c=No → `"sandbox": { "dind": true }` (no `enabled`/`azure` keys)
- Q9=No, 9b=No, 9c=Yes → `"sandbox": { "azure": true }` (no `enabled`/`dind` keys)
- Q9=Yes, 9b=Yes, 9c=Yes → `"sandbox": { "enabled": true, "dind": true, "azure": true }`
- All No → omit `sandbox` entirely (same pattern as `cicd`)

On re-configuration, merge into any existing `sandbox` object rather than replacing it wholesale — this preserves the sibling keys when only one answer changes (e.g. a dind-only re-config that answers 9b=No must drop only `dind` and retain an already-enabled `sandbox.enabled`/`azure`, and so on for each sibling).

> **Not the same as `.claude/settings.json`'s `sandbox.enabled`.** Step 4 in `phases/guidance-settings.md` always writes `"sandbox": { "enabled": false }` into `.claude/settings.json` — that key disables **Claude Code's own host sandbox**, because the cenci-sandbox container is the security boundary instead. This `.cenci/config.json` `sandbox` field is unrelated: it's this ticket's per-repo `.cenci/Dockerfile` toggle, consumed by cenci's configure skill and by `cenci sandbox build`'s per-repo image build — not by Claude Code itself. Same field name (`sandbox.enabled`), two different files, two different consumers, two unrelated meanings. Do not conflate them when reading or writing either file.
