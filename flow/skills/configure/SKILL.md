---
name: configure
description: "Configure cenci's neutral project core and generate Claude/Codex adapters."
compatibility: Requires Claude Code settings, plugin environment variables, and AskUserQuestion.
argument-hint: [--all] [additional context]
user-invocable: true
disable-model-invocation: true
allowed-tools: Read, Write, Edit, Glob, Grep, AskUserQuestion, Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/detect-project.sh"), Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/merge-sandbox-config.sh":*), Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/configure/scripts/resolve-delta.sh":*), Bash(bash "${CLAUDE_PLUGIN_ROOT}/scripts/migrate-project-core.sh":*), Bash(pen interactive --help:*), Bash(test:*), Bash(which:*), Bash(jq:*), Bash(mv ~/.claude/settings.json.tmp ~/.claude/settings.json), Bash(mkdir -p:*), Bash(rm -f .claude/hooks/check-pending-plans.sh), Bash(rmdir .claude/hooks:*), Bash(gh auth status:*), Bash(gh label list:*), Bash(gh label create:*), Bash(gh pr create:*), Bash(gh pr view:*), Bash(git rev-parse:*), Bash(git add:*), Bash(git commit:*), Bash(git worktree add:*), Bash(git -C:*), Bash(git diff --no-index:*), Bash(rm -f ${TMPDIR:-/tmp}/cenci/cenci-configure-:*), Bash(cenci dispatch status:*), Bash(cenci dispatch enroll:*), Bash(cenci dispatch plan-refined on), Bash(cenci dispatch plan-refined off), Bash(cenci dispatch plan-refined status), Bash(cenci automerge on), Bash(cenci automerge off), Bash(cenci automerge status), Bash(cenci planning attended on), Bash(cenci planning attended off), Bash(cenci planning attended status --json)
---

> **Client dispatch**: In Codex, read `codex-runtime` and `configure/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`. This also governs the `phases/*.md` and `references/*.md` files this skill reads.

## Task

Help the user set up this project for the cenci plugin.

### Parse `$ARGUMENTS`

`$ARGUMENTS` is optional. `--all` is a reserved leading token of `$ARGUMENTS`; everything after it is user context.
When the first token is `--all`, the run replays every question (see `### Delta Resolution` in `phases/detection-and-delta.md`); a `--all` anywhere
else is ordinary user context. The remaining text is optional **user context** (additional instructions or focus
areas). Strip the `--all` token before deriving the slug (see Create Worktree below).
If empty, proceed normally with defaults.

### Existing Config Detection

Resolve project configuration in this order:

1. `.cenci/config.json` is canonical. If present, read it as `existingConfig`.
2. Otherwise read `.claude/config.json` as `legacyConfig`. Treat this as a migration run.
3. If neither exists, set `existingConfig` to null.

When `.claude/config.json` exists (a legacy-only migration run, or alongside `.cenci/config.json`), the unknown-key preservation, the `migrationBase` merge and the preview/`--apply` step are covered by the migrations row of `### Step Map` below.

When `existingConfig` (or `migrationBase`) is present, tell the user before starting questions, according to the run mode (resolved in `### Delta Resolution` in `phases/detection-and-delta.md`):
- **`delta` run** (the default): "Found existing configuration. Only new or never-offered questions will be asked; answers you already gave are carried through. Run `/cenci:configure --all` to re-open every question."
- **`--all` or `full` run**: "Found existing configuration. Each question will show your current setting as the default — select it to keep it unchanged."

Reconfiguration runs are how a project picks up configure features added since its config was written. The plugin's SessionStart hook (`hooks/scripts/check-config-staleness.sh`) compares `existingConfig.configVersion` against the installed flow plugin version and nudges the user to re-run this skill when the config is stale or unstamped; completing this run refreshes the stamp (step 6) and clears the nudge.

### Create Worktree

`/cenci:configure` writes files into the repo (`.cenci/config.json`, `AGENTS.md`/`CLAUDE.md`, `.mcp.json`, `.lsp.json`, `.gitignore`, `.claudeignore`, `.github/workflows/ci.yml`, `.cenci/Dockerfile`, `.lazyboards.yml`, `.codex/agents/`) and updates `.claude/settings.json`. Like every other change in this repo, these ship as a PR — configure never writes directly to the main worktree (see `cenci:worktrees` and `docs/git-workflow.md`).

Create the worktree now, before any file is written (including a migration `--apply`, see `references/migrations.md` when `.claude/config.json` exists) and before the detection/question steps in the Step Map below, since none of them depend on it existing yet:

1. Verify at least one commit exists: `git rev-parse HEAD 2>/dev/null`. If the repository has no commits, create an initial commit as two standalone Bash calls (never compound `&&` — shell-rules: every segment of a compound is evaluated independently by the approval system): `git add -A`, then `git commit -m "chore: initial commit" --allow-empty`.
2. Derive a slug: `init` when `existingConfig` is null (first-ever configure run), `update` for a plain reconfiguration, or a short kebab-case description of the user's focus when the user context (the `$ARGUMENTS` text left after the `--all` token is stripped) names one. A bare `--all` carries no focus, so it also derives `update`. User context never re-opens an already-answered question — only `--all` does (see `### Delta Resolution` in `phases/detection-and-delta.md`).
3. Create the worktree: `git worktree add .worktrees/configure-<slug> -b chore/configure-<slug> main`. If that branch/directory name is already taken by an unrelated prior run, append `-2`, `-3`, etc. until it's free.

From this point on, `<worktree-path>` is `.worktrees/configure-<slug>`. Every file this skill reads or writes below — `.cenci/config.json`, `AGENTS.md`, `CLAUDE.md`, `.mcp.json`, `.lsp.json`, `.gitignore`, `.claudeignore`, `.claude/settings.json`, `.github/workflows/`, `.cenci/Dockerfile`, `.lazyboards.yml`, `.codex/` — and every "the repo root" / "the project root" reference in the steps below resolves against `<worktree-path>`, never the main checkout. Use absolute paths rooted at `<worktree-path>` for every Write/Edit; verify the CWD before Bash commands rather than relying on a single `cd` persisting across calls. `gh label create` / `gh issue` calls (step 3c) are GitHub API calls, not file writes, and run the same regardless of worktree.

### Step Map

Read these files in this order. The first four phase files are read in full on every run: the questions inside stay gated by `delta.ask`, but the generation steps consume the catalogs and mapping tables they hold, and the manual detection fallbacks must stay reachable when `detect-project.sh` fails. The migrations row and the four rows after the Dockerfile questions are conditional; a skipped file mutates nothing. The last three rows are read on every run, after the questions. Generation steps 0 to 5c-bis run from `phases/guidance-settings.md`, steps 5d and 5e from `phases/ci-dockerfile.md`, and step 5f from `phases/lazyboards.md`; only steps 6 and 7 stay inline in this file. Questions 1 to 9c, 10 to 17 and 18 to 20 come from the phase files and are asked as `### Question Defaults` describes, in the order 10, 11/12, 13 to 17, 18 to 20.

| File | Condition | Why |
|---|---|---|
| `references/migrations.md` | Only when `.claude/config.json` exists: a legacy-only migration run, or both config files present so `migrationBase` is merged. A `.cenci/config.json`-only run never reads it. | Unknown-key preservation, the `migrationBase` merge, and the `migrate-project-core.sh` preview/`--apply` step. |
| `phases/detection-and-delta.md` | Every run, after Create Worktree and before the first question. | `### Scripted Detection`, `### Delta Resolution` (which question IDs this run asks), and the platform and container detections with their manual fallbacks. |
| `phases/project-questions.md` | Every run, after `phases/detection-and-delta.md`. | Questions 1 to 3: tech stack, project structure with Steps 2a and 2b, branching strategy. |
| `phases/mcp-lsp-ui-questions.md` | Every run, after `phases/project-questions.md`. | Dependency detection and its manual fallback, the MCP and LSP catalogs, questions 5 and 6, UI conventions (5c), Playwright CLI setup, LSP detection and binary verification. |
| `phases/runtime-ci-sandbox-questions.md` | Every run, after `phases/mcp-lsp-ui-questions.md`. | Questions 7 to 9c: auto-compact, subagent pin, the CI/CD question with its Stack-to-CI mapping table, and the sandbox, dind and Azure questions with the Stack-to-fragment mapping table. |
| `phases/lazyboards.md` | Only when `lazyboards` is asked this run, or `.lazyboards.yml` exists at the repo root. Check existence with `test -f .lazyboards.yml` as its **own** Bash call, after `phases/runtime-ci-sandbox-questions.md`. If question 10 is carried but `.lazyboards.yml` is missing, do not regenerate it — report it and say `/cenci:configure --all` re-offers it. | Question 10 with its derivation tables and key assignment, the suggest-or-skip analyzer for an existing file, and step 5f, which generates it. |
| `references/lazyboards-example.md` | Only when step 5f writes `.lazyboards.yml`, or the analyzer rewrites it, and only after `phases/lazyboards.md`. | The full generated `.lazyboards.yml` worked example. |
| `phases/fleet-enrollment.md` | Only when `dispatchEnroll`, `dispatch.planRefined`, `automerge.enabled` or `planning.attended` is asked this run, and on every host run (`detection.inContainer` is not `true`): the half-enrolled question 12 repair has no ID and needs the status probe. Read it after `phases/lazyboards.md`; its `### Fleet Autonomy Controls` is asked after `phases/autonomy-settings.md`. | `### Fleet Dispatch Enrollment` (questions 11 and 12, with the container guard) and `### Fleet Autonomy Controls` (questions 18 to 20 and their Next step block). |
| `phases/autonomy-settings.md` | Only when `planning`, `automerge`, `refinement`, `refinement.confirmBeforeApply` or `refinement.automerge` is asked this run, and after `phases/fleet-enrollment.md`'s `### Fleet Dispatch Enrollment`. | Questions 13 to 17 and the marker-bounded `jq` programs step 6 applies. |
| `phases/guidance-settings.md` | Every run, after the questions and `### Auth Verification`. | Generation steps 0 to 5c-bis: Codex agent adapters, guidance and client adapters, `.lsp.json`, board lifecycle labels (3c), `.claude/settings.json` (4), MCP server configuration, `.claudeignore`, auto-compact and the subagent pin. |
| `references/settings-template.md` | Every run, from step 4 in `phases/guidance-settings.md`, before `permissions.deny` is written or healed. | The base `permissions.deny` list with its word-boundary rationale and residual gaps. |
| `phases/ci-dockerfile.md` | Every run, after `phases/guidance-settings.md`. | Generation steps 5d and 5e: CI/CD pipeline and `.cenci/Dockerfile` generation, each deciding internally whether it runs. |

A carried answer from a skipped phase file is reported verbatim by the step 7 final report.

### Question Defaults

**Default values from existing config**: When `existingConfig` is not null, each question below that is actually asked (every question on an `--all` or `full` run; only the IDs in `delta.ask` on a `delta` run — see `### Delta Resolution` in `phases/detection-and-delta.md`) MUST present the existing value as the pre-selected default (list it first, marked "(current)"). The user can accept with one click or change it. New fields not in `existingConfig` (e.g., `lspServers` when upgrading from a pre-LSP config) have no default and are asked normally.

**`AskUserQuestion` cannot pre-check `multiSelect` options** — its options only carry `label`/`description`/`preview`, no "selected by default" field. So for the two multi-select questions (5. MCP Servers, 6. LSP Servers), "pre-select" cannot mean a pre-ticked checkbox — re-asking the full multi-select on every reconfigure would force re-clicking every already-enabled server one at a time. Instead, gate behind a single Keep/Change confirmation first (see **Keep-or-change gate** below): when `existingConfig` already has a value for that field, ask one Yes/No question summarizing the current selections; only the change option drops into the full multi-select. This is a single click to keep everything unchanged, instead of one click per server.

**Keep-or-change gate** (the single home of this rule; questions 5 and 6 in `phases/mcp-lsp-ui-questions.md` point here): if `existingConfig.mcpServers` (question 5) or `existingConfig.lspServers` (question 6) is present, do not jump straight into the multi-select. Instead present the current state and ask a plain Yes/No:

"Current MCP servers: `<name>` ✓ enabled, `<name>` ✗ disabled, … . Keep these settings?" (question 6 asks "Current LSP servers: …" in the same form)
Options: "Yes — keep current settings (Recommended)", "No — let me change them"

- **Yes**: carry the existing field (`existingConfig.mcpServers` or `existingConfig.lspServers`) forward unchanged, skip the multi-select entirely.
- **No**: continue to the multi-select, sorting currently-enabled servers first so they're easiest to re-tick.

If the field is absent (first-ever configure run, or a newly-detected server not previously offered), skip the gate and ask the multi-select directly.

Under `--all`, every keep-or-change offer lists its Keep option first, marked "(Recommended)" and pre-selected: "Yes — keep current settings (Recommended)" here, "Keep current (Recommended)" for questions 13 and 15, "Keep current block(s) (Recommended)" for question 14 (see `### Autonomy Settings` in `phases/autonomy-settings.md`).

The `ID` column is the question's identifier in `question-keys.json` — the value `resolve-delta.sh` returns in `delta.ask`/`delta.carried` and step 6 stores in `configureOffered`. IDs are a stable contract persisted in users' ledgers.

| Question | ID | `existingConfig` field | Default when field exists |
|---|---|---|---|
| 1. Tech stack | `stack` | `stack` | Pre-fill with formatted stack |
| 2. Project structure | `isMonorepo` | `isMonorepo` | Pre-select based on existing value |
| 3. Branching strategy | `branchPattern` | `branchPattern` | Pre-fill with existing pattern |
| 5. MCP Servers | `mcpServers` | `mcpServers` | Keep-or-change gate (see **Keep-or-change gate** above); only "change" enters the multi-select, pre-sorted with enabled servers first |
| 5c. UI conventions | `uiConventions` | *(none — guidance prose only; ledger-tracked)* | Conditional on a detected frontend framework; a carried answer keeps the existing `## UI Conventions` section |
| Playwright CLI setup | `playwrightCli` | `playwrightCli` | Conditional on a frontend framework plus `detection.playwrightTest`; pre-select Yes/No |
| 6. LSP Servers | `lspServers` | `lspServers` | Keep-or-change gate (see **Keep-or-change gate** above); only "change" enters the multi-select, pre-sorted with enabled servers first |
| 7. Auto-compact | `autoCompactDisabled` | `autoCompactDisabled` | Pre-select Yes/No |
| 7b. Pin subagents to 200K | `pinSubagents200K` | `pinSubagents200K` | Pre-select Yes/No |
| 8. CI/CD pipeline | `cicd` | `cicd` | Pre-select Yes/No based on `cicd.enabled` |
| 9. Sandbox Dockerfile | `sandbox.enabled` | `sandbox` | Pre-select Yes/No based on `sandbox.enabled` |
| 9b. Nested Docker (dind) | `sandbox.dind` | `sandbox` | Pre-select Yes/No based on `existingConfig.sandbox.dind` (else Yes when a Testcontainers/Docker-SDK trigger was detected, No otherwise) |
| 9c. Azure CLI | `sandbox.azure` | `sandbox` | Pre-select Yes/No based on `existingConfig.sandbox.azure`; if field absent, ask normally |
| 10. Board config (lazyboards) | `lazyboards` | `lazyboards` | Only asked when no `.lazyboards.yml` exists; if one exists, the suggest-or-skip analyzer reports (and prompts only under `--all`/`full`) — see `### Board Config (lazyboards)` in `phases/lazyboards.md` |
| 11. Fleet dispatch enrollment | `dispatchEnroll` | *(none — user-level `~/.config/cenci/config.json`; ledger-tracked)* | Asked only when `cenci dispatch status --json` reports `enrolled: false` (see `### Fleet Dispatch Enrollment` in `phases/fleet-enrollment.md`) |
| 13. Plan approval autonomy | `planning` | `planning` | Delta run: asked only when the key is absent and not already offered; `--all` re-offers it with the committed value pre-selected (see `### Autonomy Settings` in `phases/autonomy-settings.md`) |
| 14. Automerge policy block | `automerge` | `automerge` (top-level or any `projects[]` entry) | Delta run: asked only when absent and not already offered; `--all` re-offers it with "Keep current block(s) (Recommended)" pre-selected (see `### Autonomy Settings` in `phases/autonomy-settings.md`) |
| 15. Refinement autonomy | `refinement` | `refinement` | Delta run: asked only when the key is absent and not already offered; `--all` re-offers it with "Keep current (Recommended)" pre-selected (see `### Autonomy Settings` in `phases/autonomy-settings.md`) |
| 16. Refinement confirmation | `refinement.confirmBeforeApply` | `refinement.confirmBeforeApply` | Independent leaf; default false; `--all` re-offers |
| 17. Refinement merge grants | `refinement.automerge` | `refinement.automerge` | Independent leaf; default off; `--all` re-offers |
| 18–20. Fleet autonomy controls | `dispatch.planRefined`, `automerge.enabled`, `planning.attended` | ledger only | Independent host CLI choices; sandbox defers host execution |

Ask these questions one at a time using the `AskUserQuestion` tool (on a `delta` run, only those whose ID is in `delta.ask` — skip the rest and carry their recorded values):

### Auth Verification

Before generating config, verify CLI authentication:

Run `gh auth status` and check it returns authenticated. If not, instruct the user to run `gh auth login` first.

After gathering answers:

0 to 5c-bis. **Generate guidance, client adapters, board labels, settings and MCP configuration**: run steps 0 to 5c-bis in `phases/guidance-settings.md`; its step 4 reads `references/settings-template.md`.

5d and 5e. **Generate CI/CD pipeline and `.cenci/Dockerfile`**: run steps 5d and 5e in `phases/ci-dockerfile.md`; each step decides internally whether it runs.

5f. **Generate `.lazyboards.yml`**: run step 5f in `phases/lazyboards.md` when that file was read this run.

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

```json
{
  "configVersion": "0.22.0",
  "branchPattern": "feature/<id>-<description>",
  "stack": {
    "backend": "dotnet10",
    "frontend": "angular21",
    "testing": ["xunit", "jasmine"]
  },
  "guidanceLocation": "AGENTS.md",
  "mcpServers": {
    "context7": true,
    "angular": false,
    "primeng": false
  },
  "lspServers": {
    "typescript": true,
    "csharp-ls": true
  },
  "autoCompactDisabled": true,
  "pinSubagents200K": true,
  "cenci": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "implementerConcurrency": "parallel",
    "implementerMaxConcurrency": 2,
    "diffContextMode": "inline",
    "liteReviewEnabled": true,
    "planComment": false,
    "gateOutputLines": 120
  },
  "planning": { "autonomy": "interactive" },
  "refinement": { "autonomy": "interactive" },
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  },
  "sandbox": {
    "enabled": true,
    "dind": true,
    "azure": true
  },
  "lazyboards": {
    "enabled": true,
    "serveCommand": "ng serve",
    "boardKey": "S"
  }
}
```

(The `planning` and `refinement` values shown above are illustrative of the schema, not something this skill
writes by default: question 13 (`### Autonomy Settings` in `phases/autonomy-settings.md`) writes `planning.autonomy` only when you explicitly choose `lean` (or switch under `--all`), and question 15 (`### Autonomy Settings` in `phases/autonomy-settings.md`) writes `refinement.autonomy` only when you explicitly choose `lean` (or switch under `--all`); a `planning` or `refinement` key already present in `existingConfig` is left verbatim
on a delta run; `--all` re-offers it. Declining — keeping `interactive` — writes nothing, since a missing key already resolves to `interactive`.)

`configVersion` records the flow plugin version that last wrote this config (managed by this skill — always overwritten with the current plugin version in step 6, never a question). Consumers:
- the plugin's SessionStart hook (`hooks/scripts/check-config-staleness.sh`) — nudges the user at session start when the stamp's major.minor is behind the installed plugin (or missing entirely), so they learn a `/cenci:configure` re-run would pick up new configure features without watching the changelog;
- maintain's `config-version` check (`/cenci:maintain`, via the same staleness resolver) — reports the same staleness as an advisory `warn`.

Both consumers name the unconditional questions a re-run would ask, computed by `scripts/resolve-delta.sh` from the committed `question-keys.json` table (tested by its own `scripts/resolve-delta.test.sh`). The configure flow calls this resolver itself — see `### Delta Resolution`.

Patch-only drift is deliberately silent: configure features that warrant a re-run land in minor (`feat`) or major bumps per the plugin-version-bump workflow. Do **not** add `configVersion` to the migration-removal list below — it is a managed field, not a legacy one.

The `cenci` field is optional. If present, preserve existing user values during reconfiguration. Schema:
- `compactImplementation` — exactly `true` allows small S, low-risk tickets with one delegated lane to combine red/green/refactor into one implementer subagent turn while preserving all TDD/reporting gates, lint, reuse checks and UI evidence. Default: `false`; keep an existing repository opt-in on reconfiguration.
- `reviewConcurrency` — `"parallel"` runs security, code, and silent-failure reviews together; `"sequential"` runs the same reviews one after another to smooth usage limits. Default: `"parallel"`.
- `implementerConcurrency` — `"parallel"` (default) runs eligible planner-declared `### Parallel Lanes` within each dependency wave; `"sequential"` runs one worker per preflight lane in wave order. The planner always records a `### Parallelism Assessment`; missing/unsafe declarations stay sequential. Quality gates are identical, including UI/sensitive restrictions and one combined verification barrier after all waves. The setting never creates lanes on its own.
- `implementerMaxConcurrency` — positive integer, default `2`, limiting simultaneous implementers further to available worker slots. Invalid values warn and use `2`; sequential mode always uses one. Preserve a configured value on reconfiguration. Shared foundations can precede independent consumers, but every file has one owner across all waves and consumers only read foundation files.
- `diffContextMode` — `"inline"` passes small diffs directly to reviewers; `"file"` writes the diff to this implement run's artifact directory (`$RUN_DIR/diff.patch` — see implement Phase 6 + 7) and passes paths so reviewers read targeted hunks. Default: `"inline"`.
- `liteReviewEnabled` — `true` (default) lets Phase 6 + 7 classify each diff into `full` (all three reviewers), `lite-docs` (no reviewers, docs-only), or `lite-small` (`code-reviewer` only, small config/data-only diffs); `false` forces the full trio on every run regardless of diff size or content. See Phase 6 + 7 for the precedence-ordered classification rules.
- `planComment` — `true` makes implement Phase 1 also post the saved plan as a ticket comment (ticket mode only) right before marking the ticket `Planned` (the label call stays the last ticket edit — it records the plan-freshness baseline), for audit / off-host visibility; `.plans/` stays the executable source of truth. Default: `false` (no comment).
- `gateOutputLines` — the number of trailing lines of a gate command's combined stdout+stderr that `hooks/scripts/run-gate.sh` prints (see `docs/health-gates.md`); the full untruncated output stays retrievable at `GATE_LOG` on a red gate. Default: `120`.

Configure always writes `sandbox: { "enabled": false }` in `.claude/settings.json` (no `network`/`excludedCommands`/`autoAllowBashIfSandboxed`) — the cenci-sandbox container is the security boundary. The config no longer carries `profile` or `sandboxEnabled` fields; re-config strips them from older configs (see the migration-removal list below).

Optional external usage reducer: RTK (`https://github.com/rtk-ai/rtk`) can compress shell command output before it reaches Claude Code. It is not required for cenci and should not be installed automatically, but it is worth recommending when users are hitting usage limits from command-heavy sessions. After separate installation, `rtk init -g` enables Claude Code Bash command rewriting where supported. Built-in tools like `Read`, `Grep`, and `Glob` do not pass through RTK hooks.

The `cicd` field is only present when the user selected Yes in question 8. Schema:
- `cicd.enabled` — `true` if user opted in, omit `cicd` entirely if declined
- `cicd.platform` — `"github-actions"`

Omit `cicd` entirely when the user says No.

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

The `lazyboards` field is present when question 10 was answered Yes **or** a
`.lazyboards.yml` already existed (the suggest-or-skip branch also records
`enabled: true`). Schema:
- `lazyboards.enabled` — `true` if a board config exists (generated or pre-existing); omit `lazyboards` entirely when the user declines question 10 and no file exists (same pattern as `cicd`/`sandbox`)
- **Single project**: `lazyboards.serveCommand` + `lazyboards.boardKey` record the generated serve action, and `lazyboards.testCommand` + `lazyboards.testKey` record the generated test action (command and its key — a single letter, or a multi-key mnemonic sequence like `"S b"`/`"T f"` in a monorepo). Omit the test pair when the project is not testable. `W` (Open worktree) is never recorded here — it carries no command and isn't project-specific.
- `boardKey`/`testKey` are written in lazyboards' **canonical space-separated** sequence form (`"S b"`, `"T f"`) — the same spelling the generated `.lazyboards.yml` uses. A concatenated value read from `existingConfig` (`"Sb"`, `"Tf"`) is the **same key**, not a different one: normalize it to the space-separated form on write rather than treating it as a distinct binding or as a key collision. A single-letter key (`"S"`, `"T"`) is already canonical and is written unchanged.
- **Monorepo**: `serveCommand`/`boardKey` and `testCommand`/`testKey` live on each project entry in the `projects` array instead (a project gets the serve pair only when runnable and the test pair only when testable), and the top-level `lazyboards` field carries only `enabled`
- These recorded values are advisory: the suggest-or-skip analyzer re-derives serve/test commands from the derivation tables in `### Board Config (lazyboards)` in `phases/lazyboards.md`, so a config missing them still works.

Omit `lazyboards` entirely when the user says No (same pattern as `cicd`/`sandbox`).

The `security` field is optional and is **never written by a configure prompt** — there is
no question for it. It is a manually-editable escape hatch that lets a project extend
implement's Trivial-Ticket Triage sensitive-path backstop (see `skills/implement/SKILL.md`,
"Sensitive-path backstop"). Schema:
- `security.sensitivePaths` — an array of glob-pattern strings. Each pattern is matched
  whole-path / substring-style (`*` matches any characters including `/`, case-insensitive)
  against the file path(s) a trivial-candidate ticket names; a match disqualifies the ticket
  from the trivial fast path and forces full planning.

These entries are **additive to** implement's built-in default pattern list (`*auth*`,
`*payment*`, `*billing*`, `*migrat*`, `*secret*`, `*credential*`, `*oauth*`, `*token*`, and
more) — they extend it and never replace it, so the defaults apply even when `security` is
absent. Add patterns here only to cover project-specific sensitive areas the defaults miss
(e.g. a domain-specific module name).

Because the config write below uses merge semantics (step 6 — start from `existingConfig` and
overwrite only the fields the skill manages), a hand-added `security` block is **preserved
untouched** across re-configuration. Do **not** add `security` to the migration-removal list
below — it is a supported optional field, not a legacy one.

The `planning` field is optional. `### Autonomy Settings` question 13 in `phases/autonomy-settings.md` asks for it only
when absent, defaulting to `interactive`. A key already present is reported verbatim and is never re-prompted, narrowed, or removed
on a delta run; `--all` re-offers it with the committed value pre-selected. Merge semantics (step 6) preserve a
hand-added or previously-written value untouched across reconfiguration unless you choose a switch under `--all`. It is a
safety/checkpoint toggle read by the implement skill's Phase 1 (see `skills/implement/SKILL.md`
and `skills/implement/phases/phase-1-plan.md`'s `## Lean Approval Path`). Schema:
- `planning.autonomy` — `"lean"` or `"interactive"`, default `"interactive"`. Only the exact
  string `"lean"` changes behavior — a missing `planning` block, a missing `autonomy` key, or
  any other value all resolve to `"interactive"` (the planner resolves repo-grounded choices,
  asks remaining consequential or unresolved questions via `AskUserQuestion`, and stops for
  human plan review). Both modes use `agents/planner.md`'s `## Self-Answer Policy`; in `"lean"`, the planner self-resolves everything except
  its three stop classes (security-sensitive, destructive or irreversible, scope blowup), and
  decides the contradiction and product-ambiguity questions it can justify from the repo
  (recorded as `decided:` entries, announced in one ticket comment and the PR body; a
  contradiction or ambiguity with exactly one grounded resolution is still self-resolved
  silently), and a plan persisted with no escalations — including an
  `L` estimate carrying a reasoned `None — <rationale>` no-split verdict — is implicitly
  approved and continues straight into Phase 2 in the same session.

  `planning.autonomy` has a second consumer besides `skills/implement/phases/phase-1-plan.md`:
  `cenci dispatch`'s pickup gate (`watch/internal/dispatch/autonomy.go`) reads this same
  `.cenci/config.json` field to authorize an unattended planning pickup or autonomous
  re-plan (`dispatch.planRefined: true`) — only the exact string `"lean"` authorizes
  either consumer; both default-deny identically on a missing block, missing key, or any
  other value. Unlike the implement-side consumer above (which reads the local working
  tree), the dispatch-side consumer reads this field from the remote-confirmed
  `refs/remotes/origin/main` object, never local `HEAD` or the working tree, and only
  when that pass's `git fetch origin` actually succeeded (#877) — a committed-but-unpushed
  `planning` block on local `main` can never authorize unattended planning, and a fetch
  failure holds the gate with a distinct retryable reason rather than falling back to
  stale local state. See [`watch/README.md`'s Planning pickup and autonomous
  re-plan](../../../watch/README.md#planning-pickup-and-autonomous-re-plan) for the
  dispatch-side gate.

Because the config write below uses merge semantics, a hand-added `planning` block is
**preserved untouched** across re-configuration, exactly like `security`. Do **not** add
`planning` to the migration-removal list below — it is a supported optional field, not a
legacy one.

The `refinement` field is optional. `### Autonomy Settings` question 15 in `phases/autonomy-settings.md` asks for it only when absent
(recommending `lean`, while an absent key still resolves to `interactive`). A key already present is reported verbatim and is never re-prompted, narrowed, or removed
on a delta run; `--all` re-offers it with the committed value pre-selected. Merge semantics (step 6) preserve a
hand-added or previously-written autonomy untouched across reconfiguration unless you choose a switch under `--all`; independent leaf answers preserve its siblings.
`refinement.autonomy` is consumed by `skills/refine/SKILL.md`. Schema:
- `refinement.autonomy` — `"lean"` or `"interactive"`, default `"interactive"`. Only the exact
  string `"lean"` changes behavior — a missing `refinement` block, a missing `autonomy` key, or
  any other value all resolve to `"interactive"` (questions only when consequential or unresolved). In `"lean"`,
  `/cenci:refine` asks one round of at most 3 questions and records grounded decisions
  as A1…An in the refinement summary. Resolved questionnaires apply immediately unless
  `refinement.confirmBeforeApply` explicitly requires confirmation.

Unlike `planning.autonomy`, it is read from the local `.cenci/config.json` through `project-core`, never from `origin/main`, and no fleet switch exists for it, so there is no dispatch-side consumer. Refinement autonomy controls question selection only; confirmation and automerge grants use questions 16 and 17 independently. Because the config write below uses merge semantics, a hand-added `refinement` block is
**preserved with carried siblings untouched** across re-configuration; questions 16 and 17 update only their answered leaves. Do **not** add `refinement` to the migration-removal list below — it is a supported optional field, not a legacy one.

Two independent optional leaves share that local `refinement` object:

- `refinement.confirmBeforeApply` — boolean, default `false`. False applies once the
  questionnaire is resolved; true retains the final confirmation. Invalid values
  resolve to true with a notice. Question 16 can write either boolean explicitly.
- `refinement.automerge` — `"off"` or `"eligible"`, default `"off"`. Invalid values
  resolve to off with a notice. Question 17 controls grants for eligible tickets,
  independently of both planning and refinement question modes. This is separate
  from the top-level `automerge` risk policy and the host's `automerge.enabled` flag.

**Monorepo config** — when `isMonorepo` is true, add `isMonorepo` and `projects` fields:

```json
{
  "configVersion": "0.22.0",
  "isMonorepo": true,
  "projects": [
    {
      "slug": "api",
      "path": "packages/api",
      "name": "API",
      "description": "REST API backend",
      "stack": { "framework": "dotnet10", "testing": "xunit" },
      "buildCommand": "dotnet build",
      "testCommand": "dotnet test",
      "lintCommand": "dotnet format --verify-no-changes",
      "gateCommand": "dotnet build && dotnet test",
      "serveCommand": "dotnet run",
      "boardKey": "S b",
      "testKey": "T b"
    },
    {
      "slug": "web-client",
      "path": "apps/web-client",
      "name": "Web Client",
      "description": "Angular frontend",
      "stack": { "framework": "angular21", "testing": "jasmine" },
      "buildCommand": "npm run build",
      "testCommand": "npm test",
      "lintCommand": "ng lint",
      "gateCommand": "npm run build && npm test -- --watch=false",
      "serveCommand": "ng serve",
      "boardKey": "S f",
      "testKey": "T f"
    }
  ],
  "lazyboards": {
    "enabled": true
  },
  "autoCompactDisabled": true,
  "pinSubagents200K": true,
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  },
  "sandbox": {
    "enabled": true
  }
}
```

Each project entry's `lintCommand` is optional — it is omitted for stacks with no Lint row in the Stack-to-CI mapping table (e.g. `markdown-shell`, `docker-shell`), the same way `buildCommand`/`testCommand` are already handled for stacks without one.

Each project entry's `gateCommand` is optional — unlike `lintCommand`, its presence isn't tied to the Stack-to-CI mapping table; it may simply be omitted for any project. Like the other `<verb>Command` fields, its value is executed as a shell command, so it must come only from trusted project configuration, never from untrusted input.

`babysitInterval` is an optional field — there is **no configure question for it**; it is a manually-editable escape hatch, like `security` above, and merge semantics (step 6) preserve a hand-added value untouched across reconfiguration. It sets the polling cadence for the `cenci babysit` supervisor that implement Phase 9 auto-launches (and that the standalone `babysit` skill uses), resolved the same way as `gateCommand` — a top-level `babysitInterval` for a single-project repo, or a per-`projects[]` `babysitInterval` in a monorepo. Its value is a Go duration string (e.g. `"15m"`, `"30m"`, `"1h"`); when unset (or when no config file exists) `cenci babysit` falls back to its built-in `15m` default. Both forms are valid:

```json
{ "babysitInterval": "20m" }
```

```json
{ "isMonorepo": true, "projects": [ { "slug": "api", "path": "packages/api", "babysitInterval": "5m" } ] }
```

Existing single-project configs (no `isMonorepo` field) work unchanged.

The `automerge` field is optional. `### Autonomy Settings` question 14 in `phases/autonomy-settings.md` offers to
scaffold a conservative starter block only when absent (top-level or per-`projects[]`).
A block already present is reported verbatim and is never re-prompted, narrowed, or removed
on a delta run; `--all` re-offers it ("Keep current block(s) (Recommended)" or "Remove the block(s)"). Merge semantics (step 6) preserve a hand-added or
previously-scaffolded value untouched across reconfiguration unless "Remove the block(s)" is chosen under `--all`. Do **not** add
`automerge` to the migration-removal list below — it is a supported optional field, not a
legacy one.

`automerge` is split across two config locations, each with a distinct scope and default:

- `automerge.enabled` — the fleet-wide kill switch, read from `~/.config/cenci/config.json`
  (or `$XDG_CONFIG_HOME/cenci/config.json`), **not** `.cenci/config.json`. Default `false`;
  the `cenci babysit` supervisor never evaluates automerge for any repo until this is
  explicitly set `true`.
- A per-repo `automerge` block in this repo's `.cenci/config.json` — top-level for a
  single-project repo, or per-`projects[]` entry in a monorepo (falling back to the
  top-level block when a project sets none). Schema:
  - `automerge.protectedPaths` — an array of glob-pattern strings (`*` matches any
    characters including `/`, case-insensitive, same as `security.sensitivePaths`).
    Unlike `security.sensitivePaths`, each pattern here is anchored against the
    **whole repo-relative path from the root** (not substring-matched), and a pattern
    ending in `/` with no trailing `*` matches that directory and everything under it
    — see `docs/autonomous-loop.md`. A changed file matching any pattern denies
    automerge for that PR. Absent means an empty denylist — a genuine "nothing is
    protected" statement, not a malformed block.
  - `automerge.maxChangedFiles` — required whenever an `automerge` block is present; a
    missing or non-positive value makes the whole block malformed (denied), it does **not**
    fall back to a built-in default.
  - `automerge.maxDiffLines` — required on the same terms as `maxChangedFiles` (additions +
    deletions combined). Both thresholds are the **S bound** of the ticket-size table in
    `flow/docs/ticket-sizing.md` (`10` files / `400` lines) and stay there: S is the only
    automergeable size, and the thresholds and the table change together.
  - `automerge.mergeMethod` — one of `"squash"`, `"merge"`, `"rebase"`; defaults to
    `"squash"` when omitted. The field stays readable for configuration compatibility, but
    only `"squash"` is ever executed at merge time: a `"merge"` or `"rebase"` value produces
    its own logged policy hold instead of being validated against the repo's allowed merge
    methods or executed, and is never a retry loop.

In a monorepo, a changed file is resolved to the `projects[]` entry with the longest
matching `path` prefix; that project's own `automerge` block applies, falling back to the
top-level block when the project sets none, and to the top-level block directly for files
owned by no project. When a PR's changed files span more than one applicable block, the
effective policy is the **most restrictive** merge: the minimum of each numeric cap and the
union of `protectedPaths` across every block actually touched. **Any required block
missing is a deny** — an `.cenci/config.json` that is unreadable, not valid JSON, or simply
has no applicable `automerge` key (top-level or per-project) denies automerge for that PR;
there is no fallback to built-in thresholds. This means enabling `automerge.enabled`
fleet-wide with only one `projects[]` entry's `automerge` block defined immediately denies
every PR touching files outside that project — expected, not a bug, until a top-level
block (or per-project blocks for the other projects) is added.

The per-repo block is always read from the **PR's base branch**, never the PR's own head
branch or worktree — this is deliberate: reading policy from the head ref would let a PR
widen its own `protectedPaths`/caps and self-approve, exactly the fail-open hole this
feature exists to close.

See the `automerge:ok` row of the plugin's `docs/board-labels.md` for the complementary per-ticket grant —
`.cenci/config.json`'s `automerge` block is the repo-wide risk *policy* (paths, size caps,
merge method); `automerge:ok` is the per-ticket *grant* refinement adds under
`refinement.automerge: "eligible"` when eligibility checks pass. `refinement.autonomy`
and `planning.autonomy` never imply that grant; there is no `automerge.defaultRisk`
setting. Both policy and grant must hold, alongside green CI and a mergeable PR, before
`cenci babysit` will merge.

The `maintenance` field is optional and is **never written by a configure prompt** —
there is no question for it. It is a manually-editable escape hatch, like `security`
above, and merge semantics (step 6) preserve a hand-added value untouched across
reconfiguration. Do **not** add `maintenance` to the migration-removal list below — it
is a supported optional field, not a legacy one.

Maintenance is a core cenci workflow feature and is independent of
[lazyboards](https://github.com/matteobortolazzo/lazyboards): automatic changed-file
maintenance checks run during normal `/cenci:implement` runs, with no `.lazyboards.yml`
or `lazyboards` config required. A full, on-demand audit is always available via
`/cenci:maintain`.

Schema — every field below defaults as shown, so omitting `maintenance` entirely is
equivalent to writing all four defaults explicitly:
- `maintenance.enabled` — gates optional scheduled-maintenance and reminder UX only
  (default `true`); it never disables core correctness checks
- `maintenance.checkDuringImplement` — whether Phase 8 may repair findings
  automatically (default `true`); when `false`, Phase 8 still runs its changed-file
  correctness check but carries every non-pass result forward as report-only
- `maintenance.remindAfterDays` — days between reminders to run a full
  `/cenci:maintain` audit (default `30`)
- `maintenance.generatedDocs` — whether marker-bounded generated documentation sections are checked and regenerated
  (default `true`); when `false`, generated-section maintenance is skipped without
  suppressing other checks

```json
{
  "maintenance": {
    "enabled": true,
    "checkDuringImplement": true,
    "remindAfterDays": 30,
    "generatedDocs": true
  }
}
```

Core correctness checks always run when the maintenance checker is applicable, regardless
of whether this block is present or `maintenance.enabled` is false. The block controls
optional scheduled/reminder UX, whether Phase 8 may repair versus report findings, and
whether generated documentation sections participate. Omitting it keeps all optional UX
and repair/generated-section behavior enabled by default.

The `docs` field is optional and, like `maintenance` above, has no configure question.
`docs.policy` is an optional field that no configure prompt writes, and a configure re-run preserves it together with every unrelated key.
Merge semantics (step 6) keep a hand-added value untouched; it adds no `configVersion` bump and no `question-keys.json` entry. Do **not** add `docs` to the migration-removal list below — it is a supported optional field, not a legacy one.

Schema: `docs.policy` — the exact, case-sensitive string `"minimal"` selects the minimal documentation policy for `/cenci:implement`'s Phase 8; an absent `docs` block, an absent `docs.policy`, or any other value means the default policy (an unknown value warns once, naming `docs.policy`). Example: `{"docs": {"policy": "minimal"}}`.
Plan-required and correctness documentation work always runs, while incidental documentation edits and lesson capture are what `minimal` suppresses.
`flow/README.md`'s `### Documentation policy` subsection is the user-facing reference for the resulting Phase 8 and PR-body behavior.

Only include servers in `mcpServers` that were presented as options (i.e., detected or always-available). Value is `true` if enabled, `false` if declined.

Only include servers in `lspServers` that were detected and presented in question 6. Value is `true` if enabled, `false` if declined. Omit `lspServers` entirely if no LSP servers were detected.

When migrating from an older config that has `ticketSystem`, `prSystem`, `ticketPrefix`, `adoOrg`, `adoProject`, `adoRepo`, `profile`, or `sandboxEnabled` fields, remove them during the merge.

7. **Commit, Push, and Open PR**: configure's changes live in `<worktree-path>` — ship them the same way every other change in this repo ships.

   Read `<worktree-path>/docs/git-workflow.md` for the commit/branch/PR conventions used below. Target the worktree explicitly with `git -C <worktree-path>` on every command below so they resolve against the worktree and stay auto-approved (see `cenci:shell-rules` — never compound `cd` with the git command itself).

   **Commit**:
   ```bash
   git -C <worktree-path> add -A
   git -C <worktree-path> commit -m "chore(configure): <one-line summary of what changed>

   <bullet list of generated/updated files>"
   ```
   If nothing is staged (a re-run that changed nothing), skip Commit/Push/PR entirely and report "No configuration changes — nothing to commit" instead of opening an empty PR. A stamp-only diff still opens a PR; "No configuration changes — nothing to commit" applies only when nothing is staged. For example, a run that offered a new question and recorded the decline in `configureOffered` changes `.cenci/config.json` and so is not empty.

   **Push**:
   ```bash
   git -C <worktree-path> push -u origin chore/configure-<slug>
   ```
   If push fails due to sandbox/network/auth, show the exact command and use `AskUserQuestion` ("Pushed, continue" / "Abort") to wait for the user to push manually before continuing.

   **PR**: write the body to `${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md` first (never a heredoc or large inline string), then:
   ```bash
   gh pr create --title "chore(configure): <one-line summary>" --body-file ${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md
   ```
   Body:
   ```markdown
   ## Summary
   Project configuration generated/updated by `/cenci:configure`.

   ## Changes
   - <one bullet per file/section actually written this run>

   ## Testing
   N/A — configuration/tooling only, no application code changed.
   ```
   Append a `## Review required — automerge policy` section — emitted only when question
   14 scaffolded a block this run (`### Autonomy Settings` in `phases/autonomy-settings.md`) and that block was actually written (a scaffold whose overlay fell back in step 6 emits nothing here), never on a re-config that
   reported an existing block verbatim:
   ```markdown
   ## Review required — automerge policy
   This PR scaffolds a starter `automerge` policy block (`protectedPaths`, size caps,
   `mergeMethod`). Review it before enabling automerge fleet-wide — see
   `docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch`.
   ```
   Append a `## Review required — automerge policy removed` section — emitted only when question 14 removed the block(s) this run (`--all`, "Remove the block(s)") and the removal overlay passed step 6's validation and was written, never on a run that kept, never touched, or fell back from removing an existing block:
   ```markdown
   ## Review required — automerge policy removed
   This PR removes the `automerge` policy block(s) — the top-level block and every
   `projects[]` block. Without a block, automerge is denied for every PR (deny-by-default).
   Confirm this is intended — see
   `docs/autonomous-loop.md#3-declare-what-a-merge-is-allowed-to-touch`.
   ```
   On a delta run, also append a `## Carried through (delta run)` section that lists the carried question IDs and config key names only — never config values — and says `/cenci:configure --all` would re-open them. The PR may be public and config values can be sensitive, so the verbatim `planning`/`automerge`/`refinement` report stays in the terminal:
   ```markdown
   ## Carried through (delta run)
   Question IDs carried from the existing configuration: <comma-separated `delta.carried` IDs>.
   Config keys left as-is: <comma-separated key names, e.g. `stack`, `mcpServers`, `planning`>.
   Run `/cenci:configure --all` to re-open them.
   ```
   If `gh pr create` fails because a PR for this branch already exists (re-entry after a prior turn already created it), recover the URL with `gh pr view chore/configure-<slug> --json url -q .url` and continue. For any other failure (auth, network, validation), show the exact error and use `AskUserQuestion` ("Created, continue" / "Abort") to let the user resolve it before continuing.

   Delete the PR-body temp file after a successful `gh pr create` (or a successful recovery via `gh pr view`):
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/cenci-configure-<slug>-pr-body.md
   ```

Report what was created and suggest next steps (e.g., "Try `/cenci:refine <ticket-id>` on a ticket"), including the PR URL from step 7. If question 14 (`### Autonomy Settings` in `phases/autonomy-settings.md`) scaffolded an `automerge` block this run, say so explicitly and flag it for review, then point at the fleet-side next steps: `cenci dispatch plan-refined on` (once `planning.autonomy` is `lean` on `origin/main`) and `cenci automerge on` (once the scaffolded block has been reviewed) — the same verbs named in the "Next step" block of `### Fleet Autonomy Controls` in `phases/fleet-enrollment.md`. If `sandbox.enabled` is `true`, mention the generated `.cenci/Dockerfile` and that `cenci sandbox build` (run from inside the repo, after the PR merges) builds the repo's own tailored image on top of the shared base. If `lazyboards.enabled` is `true`, list the generated `.lazyboards.yml` In Review actions with their keys, serve, and test commands (e.g., "`W` opens web-client's PR worktree, `S w` serves it with `ng serve`, `T w` runs its tests with `ng test --watch=false`") and point at `docs/orchestration.md` for the board recipe. Repeat the trust caveat here too, since this run's file ships as a PR: the generated shell actions and `cleanup` stay inert until `lazyboards trust` is run against the file's exact content, so trust must be re-granted after the PR merges if review changed a byte — and every teammate runs it once on their own clone. When the suggest-or-skip branch ran instead, report what happened (added actions, migrated to `keymaps:`, or "already complete — no changes"); on a delta run it only reports, so list the missing actions and say `/cenci:configure --all` offers to add them.

On a delta run, the final report also covers what was carried and what was refreshed. The final report repeats the carried-through summary, the Dockerfile report, and the lazyboards report. The carried-through summary names the carried question IDs and config keys and says `/cenci:configure --all` would re-open them; for a carried `planning`, `automerge` or `refinement` value it also states the existing value verbatim (terminal only, never the PR body). The Dockerfile report is the block diff (`git -C <worktree-path> diff -- .cenci/Dockerfile`) for a refreshed managed block, or "left untouched — no markers or malformed markers" with the `--all` pointer. The lazyboards report is the missing-actions list, "already covers all recommended actions", or the carried-but-missing-file notice from question 10. If the resolver failed and the run fell back to `full`, say so and why. If question 14 removed the `automerge` block(s) this run (the removal overlay was applied), say so explicitly and flag the PR's `## Review required — automerge policy removed` section. Report what was actually applied, never what was collected: when a question 13/14/15 overlay fell back, say its answer was not applied, and either that it will be asked again on the next delta run (only when its key is absent and its ID is not already in the existing ledger) or, otherwise (an `--all` change, an ID already in the existing ledger, or a present key), to re-run `/cenci:configure --all` to apply it; when the ledger overlay fell back, say the ledger was not stamped so this run's questions will be asked again; when question 11's enroll failed, report the failure and that question 11 will be asked again only when `dispatchEnroll` is not already in the existing ledger, else to re-run `/cenci:configure --all`.

### Board lifecycle labels

configure creates any missing lifecycle labels during project setup (step 3c) — `gh issue edit
--add-label` fails when a label is missing from the repository, which used to silently drop
labels (e.g. `Planned`) on projects configured before a label was introduced. The skills apply
the labels via `gh issue edit` as a ticket moves through the workflow, and self-heal with a
`gh label create … || true` before adding a label that may be missing. Document the label set
in the completion summary so the user can mirror it as columns on their board; each label's
color, description and applier live in the plugin's `docs/board-labels.md`.

`Followup` is orthogonal to the linear lifecycle above — it is never part of the `New → … → Implemented` chain and applies to a separate followup ticket (not the original). It is a capture-queue marker, never release-blocking; it is removed when the item is triaged out of the queue — promoted to real work via `/cenci:refine`, or grouped/superseded via `/cenci:maintain backlog` (see `docs/followup-triage.md`).

`Browser`, `ui:visual-check`, and `automerge:ok` are likewise refine-applied orthogonal markers, not columns in the `New → … → Implemented` chain — like `Followup`, they are not board columns, though the plugin's `docs/board-labels.md` lists them with the rest of the label set; `automerge:ok` in particular is a per-ticket grant made by `/cenci:refine` under `refinement.automerge: "eligible"` after eligibility checks; never inherited by split children or followups.

A ticket judged trivial by implement's Trivial-Ticket Triage still transits through `Planned` — same labels, same board columns as any other ticket — just collapsed into one session instead of a stop-and-relaunch between `Planned` and `Working`. No new label state is introduced by the trivial-ticket fast path.

**Reconciler-managed labels**: `dispatch-failed`, `plan-invalid`, and `reconcile-stuck` are not applied by refine or implement — cenci's dispatch reconciler applies them automatically when it detects a stuck or failed automation state (dispatched work exhausted its retry budget, a `Planned` ticket has no parseable plan file, or reconciliation itself is stuck with its apply-retry budget exhausted). Like `Followup`, they are orthogonal to the `New → … → Implemented` lifecycle above and are not columns in that chain.

`Input Needed` is likewise orthogonal — like `Followup`, it is never part of the `New → … → Implemented` chain and is not a column in it. Unlike the skill-applied lifecycle labels above (which self-heal via each skill's own `gh label create … || true` fallback), `Input Needed` is applied by implement's unattended lean-mode escalation path via the `cenci pipeline label --transition input-needed` CLI call, which self-heals the label's existence through cenci's Go `ghLabelCreate` — the same mechanism `dispatch-failed`/`plan-invalid`/`reconcile-stuck` use, not a skill-level fallback. It is removed (swapping back to `Working`) when the ticket resumes.

Lifecycle: `New → Refined → Planned → Working → In Review → Implemented`.

UI conventions — the component library, its browsable catalog, and the reuse-before-authoring rule — live in each project's `AGENTS.md` `## UI Conventions` section (question 5c), which the planner and implementer read via the plan's `## Project Context`. There is no separate design stage or design ticket.

**Migration note for existing boards** (state this when re-configuring a project that predates
`In Review`): add an **`In Review`** column/label. Previously, tickets dropped straight into
`Implemented` the moment the PR was opened; now PR-open lands them in `In Review`, and
`/cenci:babysit` promotes them to `Implemented` when the PR actually merges. Existing tickets
that carry `Implemented` from before this change but whose PRs are still open can be relabeled to
`In Review` by hand; new work follows the split automatically.

**Migration note for existing boards** (state this when re-configuring a project that predates
`Planned`): add a **`Planned`** column/label between `Refined` and `Working`. Previously, a
planning session applied `Working` at pipeline start and then stopped once the plan was saved —
so the board showed `Working` on a ticket nobody was actively working. Now a planning session
lands the ticket on `Planned` ("a plan exists on disk, ready to pick up"); the swap to
`Working` happens when the plan-file implementation run begins. New work follows this
automatically.

**Migration note for existing boards** (state this when re-configuring a project that predates
the reconciler-managed labels): re-running `/cenci:configure` on an existing project creates
the `dispatch-failed`, `plan-invalid`, and `reconcile-stuck` labels retroactively via step 3c's
missing-only `gh label create` loop. No other action is needed — the reconciler already
self-heals these labels itself via `EnsureLabels`, so a board that skips this step is not
blocked; re-running configure just creates them proactively instead of waiting for the
reconciler to hit the corresponding error state first.
