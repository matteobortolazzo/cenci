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

Reconfiguration runs are how a project picks up configure features added since its config was written. The plugin's SessionStart hook (`hooks/scripts/check-config-staleness.sh`) compares `existingConfig.configVersion` against the installed flow plugin version and nudges the user to re-run this skill when the config is stale or unstamped; completing this run refreshes the stamp (step 6 in `phases/write-config.md`) and clears the nudge.

### Create Worktree

`/cenci:configure` writes files into the repo (`.cenci/config.json`, `AGENTS.md`/`CLAUDE.md`, `.mcp.json`, `.lsp.json`, `.gitignore`, `.claudeignore`, `.github/workflows/ci.yml`, `.cenci/Dockerfile`, `.lazyboards.yml`, `.codex/agents/`) and updates `.claude/settings.json`. Like every other change in this repo, these ship as a PR — configure never writes directly to the main worktree (see `cenci:worktrees` and `docs/git-workflow.md`).

Create the worktree now, before any file is written (including a migration `--apply`, see `references/migrations.md` when `.claude/config.json` exists) and before the detection/question steps in the Step Map below, since none of them depend on it existing yet:

1. Verify at least one commit exists: `git rev-parse HEAD 2>/dev/null`. If the repository has no commits, create an initial commit as two standalone Bash calls (never compound `&&` — shell-rules: every segment of a compound is evaluated independently by the approval system): `git add -A`, then `git commit -m "chore: initial commit" --allow-empty`.
2. Derive a slug: `init` when `existingConfig` is null (first-ever configure run), `update` for a plain reconfiguration, or a short kebab-case description of the user's focus when the user context (the `$ARGUMENTS` text left after the `--all` token is stripped) names one. A bare `--all` carries no focus, so it also derives `update`. User context never re-opens an already-answered question — only `--all` does (see `### Delta Resolution` in `phases/detection-and-delta.md`).
3. Create the worktree: `git worktree add .worktrees/configure-<slug> -b chore/configure-<slug> main`. If that branch/directory name is already taken by an unrelated prior run, append `-2`, `-3`, etc. until it's free.

From this point on, `<worktree-path>` is `.worktrees/configure-<slug>`. Every file this skill reads or writes below — `.cenci/config.json`, `AGENTS.md`, `CLAUDE.md`, `.mcp.json`, `.lsp.json`, `.gitignore`, `.claudeignore`, `.claude/settings.json`, `.github/workflows/`, `.cenci/Dockerfile`, `.lazyboards.yml`, `.codex/` — and every "the repo root" / "the project root" reference in the steps below resolves against `<worktree-path>`, never the main checkout. Use absolute paths rooted at `<worktree-path>` for every Write/Edit; verify the CWD before Bash commands rather than relying on a single `cd` persisting across calls. `gh label create` / `gh issue` calls (step 3c) are GitHub API calls, not file writes, and run the same regardless of worktree.

### Step Map

Read these files in this order. The first four phase files are read in full on every run: the questions inside stay gated by `delta.ask`, but the generation steps consume the catalogs and mapping tables they hold, and the manual detection fallbacks must stay reachable when `detect-project.sh` fails. The migrations row and the four rows after the Dockerfile questions are conditional; a skipped file mutates nothing. The last six rows are read on every run, after the questions. Generation steps 0 to 5c-bis run from `phases/guidance-settings.md`, steps 5d and 5e from `phases/ci-dockerfile.md`, step 5f from `phases/lazyboards.md`, step 6 from `phases/write-config.md` (which reads `references/config-schema.md`) and step 7 from `phases/commit-pr.md`; none of them stays inline in this file. Questions 1 to 9c, 10 to 17 and 18 to 20 come from the phase files and are asked as `### Question Defaults` describes, in the order 10, 11/12, 13 to 17, 18 to 20; `### Run Order` lists the whole sequence and which run modes select each row.

| File | Condition | Why |
|---|---|---|
| `references/migrations.md` | Two sections, each read under its own condition. `## Legacy config migration`: only when `.claude/config.json` exists, a legacy-only migration run or both config files present so `migrationBase` is merged; a `.cenci/config.json`-only run never reads `## Legacy config migration`. `## Board migration notes`: only when re-configuring an existing project, a legacy-only migration run included. | Unknown-key preservation, the `migrationBase` merge, and the `migrate-project-core.sh` preview/`--apply` step; the three board migration notes (a board that predates `In Review`, `Planned` or the reconciler-managed labels). |
| `phases/detection-and-delta.md` | Every run, after Create Worktree and before the first question. | `### Scripted Detection`, `### Delta Resolution` (which question IDs this run asks), and the platform and container detections with their manual fallbacks. |
| `phases/project-questions.md` | Every run, after `phases/detection-and-delta.md`. | Questions 1 to 3: tech stack, project structure with Steps 2a and 2b, branching strategy. |
| `phases/mcp-lsp-ui-questions.md` | Every run, after `phases/project-questions.md`. | Dependency detection and its manual fallback, the MCP and LSP catalogs, questions 5 and 6, UI conventions (5c), Playwright CLI setup, LSP detection and binary verification. |
| `phases/runtime-ci-sandbox-questions.md` | Every run, after `phases/mcp-lsp-ui-questions.md`. | Questions 7 to 9c: auto-compact, subagent pin, the CI/CD question with its Stack-to-CI mapping table, and the sandbox, dind and Azure questions with the Stack-to-fragment mapping table. |
| `phases/lazyboards.md` | Only when `lazyboards` is asked this run, or `.lazyboards.yml` exists at the repo root. Check existence with `test -f .lazyboards.yml` as its **own** Bash call, after `phases/runtime-ci-sandbox-questions.md`. If question 10 is carried but `.lazyboards.yml` is missing, do not regenerate it — report it and say `/cenci:configure --all` re-offers it. | Question 10 with its derivation tables and key assignment, the suggest-or-skip analyzer for an existing file, and step 5f, which generates it. |
| `references/lazyboards-example.md` | Only when step 5f writes `.lazyboards.yml`, or the analyzer rewrites it, and only after `phases/lazyboards.md`. | The full generated `.lazyboards.yml` worked example. |
| `phases/fleet-enrollment.md` | Only when `dispatchEnroll`, `dispatch.planRefined`, `automerge.enabled` or `planning.attended` is asked this run, and on every host run (`detection.inContainer` is not `true`): the half-enrolled question 12 repair has no ID and needs the status probe. Read it after `phases/lazyboards.md`; its `### Fleet Autonomy Controls` is asked after `phases/autonomy-settings.md`. | `### Fleet Dispatch Enrollment` (questions 11 and 12, with the container guard) and `### Fleet Autonomy Controls` (questions 18 to 20 and their Next step block). |
| `phases/autonomy-settings.md` | Only when `planning`, `automerge`, `refinement`, `refinement.confirmBeforeApply` or `refinement.automerge` is asked this run, and after `phases/fleet-enrollment.md`'s `### Fleet Dispatch Enrollment`. | Questions 13 to 17 and the marker-bounded `jq` programs that step 6 in `phases/write-config.md` applies. |
| `phases/guidance-settings.md` | Every run, after the questions and `### Auth Verification`. | Generation steps 0 to 5c-bis: Codex agent adapters, guidance and client adapters, `.lsp.json`, board lifecycle labels (3c), `.claude/settings.json` (4), MCP server configuration, `.claudeignore`, auto-compact and the subagent pin. |
| `references/settings-template.md` | Every run, from step 4 in `phases/guidance-settings.md`, before `permissions.deny` is written or healed. | The base `permissions.deny` list with its word-boundary rationale and residual gaps. |
| `phases/ci-dockerfile.md` | Every run, after `phases/guidance-settings.md`. | Generation steps 5d and 5e: CI/CD pipeline and `.cenci/Dockerfile` generation, each deciding internally whether it runs. |
| `phases/write-config.md` | Every run, after `phases/ci-dockerfile.md`. | Step 6: the `.cenci/config.json` write with merge semantics, autonomy and leaf wiring, overlay order and validation, the `configVersion` stamp, the `configureOffered` ledger and the `sandbox` merge section. |
| `references/config-schema.md` | Every run, from step 6 in `phases/write-config.md`, before `.cenci/config.json` is written. | The `.cenci/config.json` examples and the field-by-field schema, ending with the legacy fields to remove on migration and the `mcpServers`/`lspServers` inclusion rules. |
| `phases/commit-pr.md` | Every run, after `phases/write-config.md`. | Step 7: commit, push and PR creation with their recovery branches, and the final report. |

A carried answer from a skipped phase file is reported verbatim by the step 7 final report in `phases/commit-pr.md`.

If a file marked `Every run` cannot be read, stop and report — never reconstruct step 6 (`phases/write-config.md`) or step 7 (`phases/commit-pr.md`) from memory.

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

The `ID` column is the question's identifier in `question-keys.json` — the value `resolve-delta.sh` returns in `delta.ask`/`delta.carried` and step 6 in `phases/write-config.md` stores in `configureOffered`. IDs are a stable contract persisted in users' ledgers.

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

### Run Order

The sequence a run follows: the questions in the order they are asked, then generation steps 0 to 7. A row's `Selected` cell says which run modes select it. A first run, an `--all` run and a `full` run (the resolver failed) select every question section; a `delta` run asks only the IDs in `delta.ask` and carries the rest. The generation steps are selected on every run, including a delta run that asks no question. A question whose detection condition fails is skipped in every run mode (see `### Delta Resolution` in `phases/detection-and-delta.md`).

| Run | File | Selected |
|---|---|---|
| Question 1 | `phases/project-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `stack` is in `delta.ask`. |
| Question 2 | `phases/project-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `isMonorepo` is in `delta.ask`. |
| Question 3 | `phases/project-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `branchPattern` is in `delta.ask`. |
| Question 5 | `phases/mcp-lsp-ui-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `mcpServers` is in `delta.ask`. |
| Question 5c | `phases/mcp-lsp-ui-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `uiConventions` is in `delta.ask`. |
| Question Playwright CLI setup | `phases/mcp-lsp-ui-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `playwrightCli` is in `delta.ask`. |
| Question 6 | `phases/mcp-lsp-ui-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `lspServers` is in `delta.ask`. |
| Question 7 | `phases/runtime-ci-sandbox-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `autoCompactDisabled` is in `delta.ask`. |
| Question 7b | `phases/runtime-ci-sandbox-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `pinSubagents200K` is in `delta.ask`. |
| Question 8 | `phases/runtime-ci-sandbox-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `cicd` is in `delta.ask`. |
| Question 9 | `phases/runtime-ci-sandbox-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `sandbox.enabled` is in `delta.ask`. |
| Question 9b | `phases/runtime-ci-sandbox-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `sandbox.dind` is in `delta.ask`. |
| Question 9c | `phases/runtime-ci-sandbox-questions.md` | On a first run, `--all` or `full` run; on a delta run only when `sandbox.azure` is in `delta.ask`. |
| Question 10 | `phases/lazyboards.md` | On a first run, `--all` or `full` run; on a delta run only when `lazyboards` is in `delta.ask`. |
| Question 11 | `phases/fleet-enrollment.md` | On a first run, `--all` or `full` run; on a delta run only when `dispatchEnroll` is in `delta.ask`. |
| Question 12 | `phases/fleet-enrollment.md` | On a host run when the status probe reports a half-enrolled repo; it has no ID, so `delta.ask` does not gate it. |
| Question 13 | `phases/autonomy-settings.md` | On a first run, `--all` or `full` run; on a delta run only when `planning` is in `delta.ask`. |
| Question 14 | `phases/autonomy-settings.md` | On a first run, `--all` or `full` run; on a delta run only when `automerge` is in `delta.ask`. |
| Question 15 | `phases/autonomy-settings.md` | On a first run, `--all` or `full` run; on a delta run only when `refinement` is in `delta.ask`. |
| Question 16 | `phases/autonomy-settings.md` | On a first run, `--all` or `full` run; on a delta run only when `refinement.confirmBeforeApply` is in `delta.ask`. |
| Question 17 | `phases/autonomy-settings.md` | On a first run, `--all` or `full` run; on a delta run only when `refinement.automerge` is in `delta.ask`. |
| Question 18–20 | `phases/fleet-enrollment.md` | On a first run, `--all` or `full` run; on a delta run only for those of `dispatch.planRefined`, `automerge.enabled` and `planning.attended` in `delta.ask`. |
| Step 0 to 5c-bis | `phases/guidance-settings.md` | Every run. |
| Step 5d and 5e | `phases/ci-dockerfile.md` | Every run; each step decides internally whether it runs. |
| Step 5f | `phases/lazyboards.md` | Every run; the step itself runs only when that file was read this run. |
| Step 6 | `phases/write-config.md` | Every run, including a delta run that asks no question. |
| Step 7 | `phases/commit-pr.md` | Every run; with nothing staged it only reports that there is nothing to commit. |

### Auth Verification

Before generating config, verify CLI authentication:

Run `gh auth status` and check it returns authenticated. If not, instruct the user to run `gh auth login` first.

After gathering answers:

0 to 5c-bis. **Generate guidance, client adapters, board labels, settings and MCP configuration**: run steps 0 to 5c-bis in `phases/guidance-settings.md`; its step 4 reads `references/settings-template.md`.

5d and 5e. **Generate CI/CD pipeline and `.cenci/Dockerfile`**: run steps 5d and 5e in `phases/ci-dockerfile.md`; each step decides internally whether it runs.

5f. **Generate `.lazyboards.yml`**: run step 5f in `phases/lazyboards.md` when that file was read this run.

6. **Write `.cenci/config.json`**: run step 6 in `phases/write-config.md`; it reads `references/config-schema.md` for the example and field-by-field schema.

7. **Commit, Push, and Open PR**: run step 7 in `phases/commit-pr.md`, which also holds the final report.

### Board lifecycle labels

Step 3c in `phases/guidance-settings.md` creates any missing lifecycle labels during project setup, because `gh issue edit --add-label` fails when a label is missing from the repository.
Each label's color, description and applier live in the plugin's `docs/board-labels.md`, the only lifecycle-label table.
Document the label set in the completion summary so the user can mirror it as columns on their board;
on a re-configuration run state the matching board migration note (see the migrations row of `### Step Map`).
