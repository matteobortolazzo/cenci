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

Read these files in this order. The first four phase files are read in full on every run: the questions inside stay gated by `delta.ask`, but the inline generation steps below consume the catalogs and mapping tables they hold, and the manual detection fallbacks must stay reachable when `detect-project.sh` fails. The migrations row and the four rows after the Dockerfile questions are conditional; a skipped file mutates nothing. Questions 1 to 9c, 10 to 17 and 18 to 20 come from the phase files and are asked as `### Question Defaults` describes, in the order 10, 11/12, 13 to 17, 18 to 20.

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

Generation steps 0 to 7 stay inline in this file, except step 5f, which runs from `phases/lazyboards.md`; a carried answer from a skipped phase file is reported verbatim by the step 7 final report.

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

0. **Generate Codex agent adapters**: copy the reviewed TOML role templates from
   `templates/codex/agent-roles/` into `.codex/agents/`. Preserve unknown user-authored agent
   files. Planning, implementation, and critical review use GPT-5.6/high; gathering and
   read-heavy analysis use GPT-5.6-terra/medium. Native procedures must fall back to built-in
   workers with the same role prompt when an adapter is unavailable.

1. **Generate canonical guidance and client adapters**:
   - `AGENTS.md` at the repository root is canonical shared guidance. Use
     `templates/agents-md-root.md` or `templates/agents-md-root-monorepo.md`.
   - For each monorepo project, generate `<project>/AGENTS.md` from
     `templates/agents-md-project.md`.
   - Generate root `CLAUDE.md` as `@AGENTS.md`, followed only by an optional block
     delimited with `<!-- cenci:claude-only:start -->` and
     `<!-- cenci:claude-only:end -->`.
   - Generate each `<project>/CLAUDE.md` as `@AGENTS.md` with the same optional block.

   Discover root `AGENTS.md`, root `CLAUDE.md`, legacy `.claude/CLAUDE.md`, and every
   configured project's AGENTS/CLAUDE pair. A retained `.claude/CLAUDE.md` adapter imports
   `@../AGENTS.md`. Before replacing any substantive existing guidance, render and show
   the complete proposed diff with `git diff --no-index`, then require approval through
   `AskUserQuestion`. Preserve existing material by merging it into the shared AGENTS
   content or the explicitly Claude-only block. Never silently delete guidance. Stable
   markers make reruns idempotent. Never delete a second substantive guidance file unless
   the user explicitly approves its content having been merged into AGENTS.md.

1b. **Generate `.lsp.json`** (if any LSP servers were selected in question 6):

   Write `.lsp.json` **in the project root** (not `${CLAUDE_PLUGIN_ROOT}`) with entries for selected servers only, using the command, args, and extension map from the `### LSP Server Catalog` in `phases/mcp-lsp-ui-questions.md`. Claude Code only reads `.lsp.json` from the project root — plugin-scoped LSP config is not supported. If `.lsp.json` already exists in the project root, merge new entries into it (preserve existing entries). Example:

   ```json
   {
     "typescript": {
       "command": "typescript-language-server",
       "args": ["--stdio"],
       "extensionToLanguage": {
         ".ts": "typescript",
         ".tsx": "typescriptreact",
         ".js": "javascript",
         ".jsx": "javascriptreact"
       }
     },
     "csharp-ls": {
       "command": "csharp-ls",
       "extensionToLanguage": { ".cs": "csharp" }
     }
   }
   ```

   Omit the `args` field if the catalog entry has an empty array `[]`.

2. Create the `docs/` directory at the repo root (if it doesn't exist) and deploy on-demand reference docs from `${CLAUDE_PLUGIN_ROOT}/templates/docs/`:
   - `docs/git-workflow.md` — branching, commit format, PR workflow

   `.claude/rules/` is reserved for files explicitly imported by the generated Claude adapter. Do NOT deploy shared reference docs there.

   **Backward compatibility**: Do NOT delete or migrate any existing `.claude/rules/lessons-learned.md`, `.claude/rules/lessons-learned-<slug>.md`, or `.claude/rules/git-workflow.md` files. Skills and agents continue to read them as legacy fallback if present.

   Do NOT deploy `testing.md` or `security.md` — testing rules load on-demand via the `testing` skill, and security rules are distributed across root/project AGENTS.md and stack skills.

**Monorepo-only additional files:**

3a. For each project, create `<project-path>/AGENTS.md` using `templates/agents-md-project.md`, then create its `CLAUDE.md` import adapter — customize with:
   - `<project-name>`: the project name (from slug or user input)
   - `<stack>`: detected stack
   - `<repo-name>`: the repository name
   - `<framework + version>`: detected framework
   - `<test-framework>`: detected test framework
   - `<build-command>`: detected or user-provided build command
   - `<test-command>`: detected or user-provided test command
   - `<project-specific rules populated during configure>`: leave as a placeholder for the user to fill in later, or remove the bullet if no conventions are known yet

3c. **Ensure board lifecycle labels exist**: `gh issue edit --add-label` fails when the label is missing from the repository, so create the lifecycle set now. Run `gh label list --repo <owner>/<repo> --limit 100 --json name` once, then for each **missing** label run its own `gh label create` call (never modify or recolor a label that already exists):

   ```bash
   gh label create "<name>" --repo <owner>/<repo> --color "<color>" --description "<description>"
   ```

   Read the plugin's `docs/board-labels.md` before the loop: its `## Labels` table is the one source of every label's `<color>` and `<description>`, and its "How existence is ensured" section says which labels are self-healed by the skills' own `gh label create … || true` fallbacks, by the `cenci pipeline label` CLI, or by the reconciler's Go `GHMutator.EnsureLabels` (not a skill-level fallback) — see "Reconciler-managed labels" under Board lifecycle labels below. If the doc cannot be read, stop and report — never guess a color or description.

   `Followup` is a capture-queue marker, never release-blocking (there is no release gate that reads labels). Captured items are triaged out of the queue by grouping/consolidation via `/cenci:maintain backlog` — which merges duplicates and batches small items, supersede-closing the sources — or promoted individually via `/cenci:refine`. See `docs/followup-triage.md`.

4. **Create or update `.claude/settings.json`**:

   Write the minimal shape below — Claude Code's host sandbox is disabled because the container is the boundary. Under `--dangerously-skip-permissions` Claude Code ignores `permissions.allow/deny`, but keep the base allow list + deny rules as defense-in-depth for the case where a user runs plain `claude` (no skip-permissions) inside the container, e.g. via `cenci open --shell`.

   ```json
   {
     "sandbox": { "enabled": false },
     "permissions": { "allow": [ … ], "deny": [ … ] }
   }
   ```

   - **Fresh** (no existing settings.json): copy `${CLAUDE_PLUGIN_ROOT}/templates/settings.json` as the base.
   - **Existing** settings.json: read it first, then **replace the entire `sandbox` block** with `{ "enabled": false }` (drop `network`, `excludedCommands`, `autoAllowBashIfSandboxed`, and any other sandbox sub-keys an older config may carry). **Preserve** every existing `permissions.allow` and `permissions.deny` entry, including user-added ones — never remove one, with the single legacy-supersession exception called out in the base deny list clause below.
   - Omit `sandbox.network`, `sandbox.excludedCommands`, and `sandbox.autoAllowBashIfSandboxed` entirely — they are meaningless when the sandbox is disabled. Never write `sandbox.network.allowedDomains`.

   Then **append** to it:

   > **IMPORTANT**: All base permissions from the template (`Write`, `Edit`, `Read(~/.claude/plugins/**)`, `Read(//tmp/claude*/**)`, `Edit(//tmp/claude*/**)`, `Read(//tmp/cenci*/**)`, `Edit(//tmp/cenci*/**)`, `Bash(cd:*)`, `Bash(git:*)`, `Bash(gh:*)`, `Bash(wc:*)`, etc.) **MUST** remain in `permissions.allow`. Only **append** new entries — never remove or replace existing ones. When updating an **existing** `settings.json`, also ensure these base entries are present — add any that are missing (older configs predate them — e.g. the `//tmp/cenci*/**` pair for the `${TMPDIR:-/tmp}/cenci/` temp-root migration). The `Read(~/.claude/plugins/**)` rule lets the pipeline read its own plugin files (phase docs resolve to `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/skills/…`) without prompting — it is deliberately scoped to `plugins/` so subagents cannot read session transcripts or global config under `~/.claude/`. The two `//tmp/*` rule pairs cover two temp roots side by side: `//tmp/claude*/**` still covers the `shell-rules` heredoc temp-file pattern's legacy paths and the Claude Code session scratchpad; `//tmp/cenci*/**` covers the canonical `${TMPDIR:-/tmp}/cenci/` root skills now write under — but only under the default `TMPDIR`, since a custom `TMPDIR` cannot be expressed as a static permission rule, so those writes may still prompt.

   > **IMPORTANT**: All base deny rules from the template **MUST** be present in `permissions.deny`. Only **append** new entries — never remove or replace existing ones, including user-added entries. When updating an **existing** `settings.json`, also ensure these base entries are present — add any that are missing (older configs predate them: they shipped only `Bash(git push --force:*)` and `Bash(git reset --hard:*)`). Two healing specifics: (a) **remove** the legacy `Bash(git push --force:*)` and `Bash(git reset --hard:*)` entries when adding the base list — the boundary-safe forms below supersede them, and the legacy `--force` form also blocks `git push --force-with-lease`, which implement's PR phase requires; (b) deduplicate — never add a base entry the list already contains. **Verify** after healing that every base deny entry is present and neither legacy entry remains.

   The deny list is written with explicit word boundaries rather than the `:*` shorthand wherever the boundary is load-bearing: `Bash(git push --force *)` blocks a bare force-push while leaving `git push --force-with-lease` usable, which implement's PR phase depends on. Deny always overrides allow, so a permitted form must never be caught by a deny rule. `git -C <path>` (used by every phase), `git rebase`, `git branch -D`, `git worktree remove`, and unscoped `git config --get`/`--list` are deliberately **not** denied. Two limits are inherent to prefix rules and are not covered: env-var-prefixed invocations (`GIT_CONFIG_GLOBAL=… git …`) never match a `git …` rule, and implicit-local `git config <key> <value>` writes are caught only for the enumerated code-execution keys.
   `-C <path>` prefix coverage is now added for the highest-value groups (force-push — scoped to the `--force`/`-f` forms only, since `push --mirror`, `push --delete`, `push --no-verify`, and the refspec-force form `push * +*` have no `-C`-prefixed mirror and remain a residual gap — reset --hard, filter-branch/filter-repo, and the `-c` exec-key rules) but not for the `git config` scope-selector write forms (`--global`, `--system`, `--local`, `--unset`, `--add`, `--replace-all`, `--edit`), and `--git-dir=`/`--work-tree=` global-flag prefixes remain entirely uncovered by any rule, old or new — both are residual, documented gaps pending empirical Claude Code Bash-matcher verification (blocked in this environment — no `claude` CLI installed).
   The config-key case-insensitivity fix covers only the four keys with non-lowercase spellings already in the rule set (`core.hooksPath`, `core.sshCommand`, `core.gitProxy`, `core.askPass`); full section-name case variants (e.g. `CORE.hooksPath`, `Alias.foo`) are not exhaustively covered.
   Beyond `-C`, other bypasses are known but not exhaustively covered: (a) arbitrary global-flag prefixes before the subcommand (e.g. `git --no-pager -c core.hooksPath=… status`) can still defeat even the mid-position `-c` forms, since only `-C` is specifically mirrored; (b) quoted or embedded arguments (e.g. `git -c 'core.pager=less -R' log`) break the literal-prefix match entirely — this is not an exhaustive bypass list, only the known ones.

   `gh api` destructive-method coverage denies `DELETE`/`PUT` (method-first and path-first, `-X`/`-XM`/`-X=`/`--method`/`--method=` forms — ten syntactic forms in total — plus lowercase mirrors) so the path-only `Bash(gh api repos/:*)` grant cannot be used to issue `DELETE`/`PUT` requests; like the `-C`/`--git-dir=` residual gaps above, this coverage is unverified pending empirical Claude Code Bash-matcher verification (blocked in this environment — no `claude` CLI installed). The mixed-case form (e.g. `-X Delete`) is an accepted residual under literal prefix matching and remains undenied, alongside `gh api graphql` mutations, `gh` aliases, env-prefixed invocations (e.g. `GH_TOKEN=… gh api …`), quoted method values (e.g. `-X "DELETE"`), and combined shorthand (e.g. `-iXDELETE`) — this residual list is illustrative of known gaps under literal prefix matching, not exhaustive. Two broader gaps remain entirely out of scope for this rule set: `Bash(gh:*)` (the pre-existing blanket allow grant) still permits native destructive `gh` subcommands that never go through `gh api` at all — e.g. `gh repo delete`, `gh release delete`, `gh secret set`, and `gh auth token` (credential exfiltration) — and the still-allowed `-X PATCH` form can itself overwrite or mutate resources (e.g. `gh api repos/o/r -X PATCH -f private=false`).

<!-- cenci:settings-deny:start -->
```json
[
  "Bash(git push --force)",
  "Bash(git -C * push --force)",
  "Bash(git push --force *)",
  "Bash(git -C * push --force *)",
  "Bash(git push * --force)",
  "Bash(git -C * push * --force)",
  "Bash(git push * --force *)",
  "Bash(git -C * push * --force *)",
  "Bash(git push -f)",
  "Bash(git -C * push -f)",
  "Bash(git push -f *)",
  "Bash(git -C * push -f *)",
  "Bash(git push * -f)",
  "Bash(git -C * push * -f)",
  "Bash(git push * -f *)",
  "Bash(git -C * push * -f *)",
  "Bash(git push * +*)",
  "Bash(git push --mirror*)",
  "Bash(git push * --mirror*)",
  "Bash(git push --delete *)",
  "Bash(git push * --delete *)",
  "Bash(git push --no-verify*)",
  "Bash(git push * --no-verify*)",
  "Bash(git commit --no-verify*)",
  "Bash(git commit * --no-verify*)",
  "Bash(git reset --hard*)",
  "Bash(git -C * reset --hard*)",
  "Bash(git reset * --hard*)",
  "Bash(git -C * reset * --hard*)",
  "Bash(git clean:*)",
  "Bash(git checkout -- *)",
  "Bash(git checkout * -- *)",
  "Bash(git restore:*)",
  "Bash(git filter-branch:*)",
  "Bash(git -C * filter-branch)",
  "Bash(git -C * filter-branch *)",
  "Bash(git filter-repo:*)",
  "Bash(git -C * filter-repo)",
  "Bash(git -C * filter-repo *)",
  "Bash(git reflog expire*)",
  "Bash(git reflog delete*)",
  "Bash(git gc --prune*)",
  "Bash(git gc * --prune*)",
  "Bash(git update-ref:*)",
  "Bash(git replace:*)",
  "Bash(git branch -f *)",
  "Bash(git branch --force *)",
  "Bash(git -c core.hooksPath*)",
  "Bash(git -c core.hookspath*)",
  "Bash(git -c * core.hooksPath*)",
  "Bash(git -c * core.hookspath*)",
  "Bash(git -C * -c core.hooksPath*)",
  "Bash(git -C * -c core.hookspath*)",
  "Bash(git -c core.sshCommand*)",
  "Bash(git -c core.sshcommand*)",
  "Bash(git -c * core.sshCommand*)",
  "Bash(git -c * core.sshcommand*)",
  "Bash(git -C * -c core.sshCommand*)",
  "Bash(git -C * -c core.sshcommand*)",
  "Bash(git -c core.pager*)",
  "Bash(git -c * core.pager*)",
  "Bash(git -C * -c core.pager*)",
  "Bash(git -c core.editor*)",
  "Bash(git -c * core.editor*)",
  "Bash(git -C * -c core.editor*)",
  "Bash(git -c core.fsmonitor*)",
  "Bash(git -c * core.fsmonitor*)",
  "Bash(git -C * -c core.fsmonitor*)",
  "Bash(git -c alias.*)",
  "Bash(git -c * alias.*)",
  "Bash(git -C * -c alias.*)",
  "Bash(git -c credential.helper*)",
  "Bash(git -c * credential.helper*)",
  "Bash(git -C * -c credential.helper*)",
  "Bash(git -c core.gitProxy*)",
  "Bash(git -c core.gitproxy*)",
  "Bash(git -c * core.gitProxy*)",
  "Bash(git -c * core.gitproxy*)",
  "Bash(git -C * -c core.gitProxy*)",
  "Bash(git -C * -c core.gitproxy*)",
  "Bash(git -c core.askPass*)",
  "Bash(git -c core.askpass*)",
  "Bash(git -c * core.askPass*)",
  "Bash(git -c * core.askpass*)",
  "Bash(git -C * -c core.askPass*)",
  "Bash(git -C * -c core.askpass*)",
  "Bash(git -c sequence.editor*)",
  "Bash(git -c * sequence.editor*)",
  "Bash(git -C * -c sequence.editor*)",
  "Bash(git -c gpg.program*)",
  "Bash(git -c * gpg.program*)",
  "Bash(git -C * -c gpg.program*)",
  "Bash(git -c diff.external*)",
  "Bash(git -c * diff.external*)",
  "Bash(git -C * -c diff.external*)",
  "Bash(git --config-env*)",
  "Bash(git --exec-path*)",
  "Bash(git config --global*)",
  "Bash(git config --system*)",
  "Bash(git config --local*)",
  "Bash(git config --file*)",
  "Bash(git config -f *)",
  "Bash(git config --unset*)",
  "Bash(git config * --unset*)",
  "Bash(git config --add *)",
  "Bash(git config * --add *)",
  "Bash(git config --replace-all*)",
  "Bash(git config * --replace-all*)",
  "Bash(git config --edit*)",
  "Bash(git config -e*)",
  "Bash(git config set *)",
  "Bash(git config unset *)",
  "Bash(git config core.hooksPath*)",
  "Bash(git config core.hookspath*)",
  "Bash(git config * core.hooksPath*)",
  "Bash(git config * core.hookspath*)",
  "Bash(git config core.sshCommand*)",
  "Bash(git config core.sshcommand*)",
  "Bash(git config * core.sshCommand*)",
  "Bash(git config * core.sshcommand*)",
  "Bash(git config core.pager*)",
  "Bash(git config * core.pager*)",
  "Bash(git config core.editor*)",
  "Bash(git config * core.editor*)",
  "Bash(git config core.fsmonitor*)",
  "Bash(git config * core.fsmonitor*)",
  "Bash(git config alias.*)",
  "Bash(git config * alias.*)",
  "Bash(git config credential.helper*)",
  "Bash(git config * credential.helper*)",
  "Bash(git config core.gitProxy*)",
  "Bash(git config core.gitproxy*)",
  "Bash(git config * core.gitProxy*)",
  "Bash(git config * core.gitproxy*)",
  "Bash(git config core.askPass*)",
  "Bash(git config core.askpass*)",
  "Bash(git config * core.askPass*)",
  "Bash(git config * core.askpass*)",
  "Bash(git config sequence.editor*)",
  "Bash(git config * sequence.editor*)",
  "Bash(git config gpg.program*)",
  "Bash(git config * gpg.program*)",
  "Bash(git config diff.external*)",
  "Bash(git config * diff.external*)",
  "Bash(git remote set-url*)",
  "Bash(git config remote.*)",
  "Bash(gh api -X DELETE*)",
  "Bash(gh api -X delete*)",
  "Bash(gh api -XDELETE*)",
  "Bash(gh api -Xdelete*)",
  "Bash(gh api --method DELETE*)",
  "Bash(gh api --method delete*)",
  "Bash(gh api --method=DELETE*)",
  "Bash(gh api --method=delete*)",
  "Bash(gh api * -X DELETE*)",
  "Bash(gh api * -X delete*)",
  "Bash(gh api * -XDELETE*)",
  "Bash(gh api * -Xdelete*)",
  "Bash(gh api * --method DELETE*)",
  "Bash(gh api * --method delete*)",
  "Bash(gh api * --method=DELETE*)",
  "Bash(gh api * --method=delete*)",
  "Bash(gh api -X PUT*)",
  "Bash(gh api -X put*)",
  "Bash(gh api -XPUT*)",
  "Bash(gh api -Xput*)",
  "Bash(gh api --method PUT*)",
  "Bash(gh api --method put*)",
  "Bash(gh api --method=PUT*)",
  "Bash(gh api --method=put*)",
  "Bash(gh api * -X PUT*)",
  "Bash(gh api * -X put*)",
  "Bash(gh api * -XPUT*)",
  "Bash(gh api * -Xput*)",
  "Bash(gh api * --method PUT*)",
  "Bash(gh api * --method put*)",
  "Bash(gh api * --method=PUT*)",
  "Bash(gh api * --method=put*)",
  "Bash(gh api -X=DELETE*)",
  "Bash(gh api -X=delete*)",
  "Bash(gh api -X=PUT*)",
  "Bash(gh api -X=put*)",
  "Bash(gh api * -X=DELETE*)",
  "Bash(gh api * -X=delete*)",
  "Bash(gh api * -X=PUT*)",
  "Bash(gh api * -X=put*)",
  "Read(~/.ssh/**)",
  "Edit(~/.ssh/**)",
  "Read(~/.aws/**)",
  "Edit(~/.aws/**)",
  "Read(.env)",
  "Edit(.env)",
  "Read(.env.*)",
  "Edit(.env.*)"
]
```
<!-- cenci:settings-deny:end -->

   **Append to `permissions.allow`:**
   - Stack-specific rules (e.g., `Bash(dotnet:*)` for .NET, `Bash(ng:*)` for Angular, `Bash(go:*)` for Go)
   - For each enabled MCP, add its tool permissions. Look up the server's available tools
     and add entries in the format `mcp__<server-name>__<tool-name>` (for project-scoped).
     Known tools:
     - **Context7**: `mcp__context7__resolve-library-id`, `mcp__context7__query-docs`
     - **Angular**: `mcp__angular__:*` (auto-allow all Angular CLI MCP tools)
     - **PrimeNG**: `mcp__primeng__:*` (auto-allow all PrimeNG MCP tools)

   **Pending-plans detection** — no per-project setup required. The pending-plans
   SessionStart hook is shipped plugin-side (`${CLAUDE_PLUGIN_ROOT}/hooks/scripts/check-pending-plans.sh`,
   registered in `flow/hooks/hooks.json`) and runs automatically wherever cenci is
   enabled. Do **not** add a SessionStart entry to `.claude/settings.json` or copy any
   script into `.claude/hooks/`.

   **Legacy cleanup (hook path)** — heal projects configured by an older cenci that installed the
   hook per-project (a fragile cwd-relative path that errored in worktrees and
   subdirectories):
   1. If `.claude/settings.json` has a `hooks.SessionStart` hook whose `command` is
      `.claude/hooks/check-pending-plans.sh`, remove that hook entry. If its enclosing
      block's `hooks` array becomes empty, remove the block too; if `SessionStart`
      becomes empty, remove it. Preserve every other SessionStart entry untouched.
   2. Delete the orphaned script if present: `rm -f .claude/hooks/check-pending-plans.sh`.
      Then remove the directory only if it is now empty: `rmdir .claude/hooks 2>/dev/null || true`
      (the `|| true` keeps it non-fatal when the dir is absent or still holds other hooks;
      run as its own Bash call, never compounded with a `cd` — see `cenci:shell-rules`).

   **Legacy cleanup (permissions)** — heal projects configured by an older cenci that shipped
   scoped-path `Write(<path>)` entries in `permissions.allow` and/or
   `permissions.deny` (Claude Code's file permission checker never matches
   scoped-path `Write(<path>)` rules — only scoped-path `Edit(<path>)` rules,
   which also cover Write — so a scoped `Write(<path>)` entry is dead weight
   that produces a startup warning per entry, in both lists):
   1. For **every** scoped-path `Write(<path>)` entry found in
      `permissions.allow` **or** `permissions.deny` (not just the known
      template rules like `Write(//tmp/claude*/**)` or `Write(~/.ssh/**)`),
      **replace** it in place with the corrected `Edit(<path>)` form rather
      than appending the missing entry alongside it. Deduplicate as you go:
      if that same list already contains an `Edit(<path>)` entry with that
      same `<path>` (the template's deny list always did), drop the redundant
      `Write(<path>)` entry instead of creating a duplicate `Edit(<path>)`.
   2. If `permissions.allow` contains a blanket `Write(*)` entry, normalize
      it to bare `Write`. (This normalization applies to `allow` only — a
      bare `Write` in `deny` would block all file writes and was never
      shipped by any template.)
   3. **Verify**: after healing, confirm neither `permissions.allow` nor
      `permissions.deny` contains a remaining scoped-path `Write(<path>)`
      entry (a blanket bare `Write` or `Write(*)`→`Write` normalization
      result in `allow` is fine — only scoped-path `Write(<path>)` forms are
      the problem). If any remain, the heal step above was incomplete — fix
      before continuing.

   **Legacy cleanup (Context7 scope migration)** — heal projects configured by an older
   cenci that granted the plugin-scoped Context7 permission pair before Context7 moved
   to project scope:
   1. Remove every `permissions.allow` entry matching the `mcp__plugin_cenci_context7__*`
      prefix. Leave every other `permissions.allow` entry untouched, including other
      `mcp__*` grants — this cleanup is scoped to the Context7 prefix only, never a
      broader `mcp__plugin_*` or `mcp__*` match.
   2. If Context7 is enabled this run, ensure the project-scoped
      `mcp__context7__resolve-library-id` and `mcp__context7__query-docs` pair is present
      in `permissions.allow` — add whichever is missing, and deduplicate rather than
      appending a duplicate entry.
   3. If Context7 is declined this run, remove the pair without adding a replacement.
   4. **Verify**: after healing, confirm `permissions.allow` contains no remaining entry
      with the `mcp__plugin_cenci_context7__*` prefix, and that every non-Context7 entry
      present before this step is still present unchanged. If either check fails, the
      heal step above was incomplete or over-broad — fix before continuing.

### MCP Server Configuration

For each MCP selected in question 5:

**Project-scoped (Context7, Angular, PrimeNG):**
- Only create or modify the project's root `.mcp.json` if at least one project-scoped MCP server was selected. Never *create* an empty `.mcp.json`.
- Create or update the project's root `.mcp.json`
- Add entries from the catalog, e.g.:
  ```json
  {
    "mcpServers": {
      "context7": {
        "command": "npx",
        "args": ["-y", "@upstash/context7-mcp@3.2.5"],
        "env": { "CONTEXT7_API_KEY": "${CONTEXT7_API_KEY}" }
      },
      "angular": {
        "command": "npx",
        "args": ["-y", "@angular/cli", "mcp"]
      },
      "primeng": {
        "command": "npx",
        "args": ["-y", "@primeng/mcp"]
      }
    }
  }
  ```
- If Context7 was selected: note to user: "Set CONTEXT7_API_KEY in your shell environment (free key from context7.com/dashboard)"
- If the file already exists, merge into the existing `mcpServers` object — never overwrite existing entries, with two field-scoped exceptions:
  - **Version-pin refresh**: for a catalog server whose catalog `Args` are version-pinned (e.g. Context7), if an existing entry's `args` differ from the catalog value, overwrite **`args` only** — every other key (including `env` and any user-added keys) is preserved. This is the single explicit, documented exception to "never overwrite existing entries."
  - **Declined servers**: for each catalog server recorded `false` in `mcpServers` (question 5's answer), delete that entry from an existing `.mcp.json` if present. If `mcpServers` becomes empty as a result, leave the file in place with an empty `mcpServers` object — never delete the file. (The "never create an empty `.mcp.json`" rule above applies to *creating* a new file only, not to editing an existing one down to empty.)

5. Update `.gitignore`:
   - Add `.worktrees/` if not present
   - Add `.plans/` if not present (plan files are ephemeral, session-specific)
   - Add `.cenci/pipeline/` if not present (transient per-run `cenci pipeline` state, anchored to the main-checkout root — see `phase-2-worktree.md`'s Gate Check)
   - Add `**/.cenci/maintain-report.json` if not present (`/cenci:maintain`'s generated report; matched at any depth since it can land in any project subdir, e.g. `watch/.cenci/`)
   - Check each entry individually before adding — skip any that are already in `.gitignore`. (If a prior cenci version appended a `# Claude Code sandbox artifacts` block, leave it in place — the entries are harmless and removing them would violate the append-only rule.)
5b. **Generate `.claudeignore`**: Create or update `.claudeignore` in the project root. This file tells Claude Code to ignore files that are tracked by git but not useful as context (binary assets, lock files, generated bundles). Claude already respects `.gitignore`, so `.claudeignore` is only for tracked files.

   - If `.claudeignore` already exists, merge new entries into it — preserve user-added entries, skip duplicates.
   - If it does not exist, create it from `${CLAUDE_PLUGIN_ROOT}/templates/claudeignore` as the base.
   - Then **append** stack-specific patterns based on the tech stack from question 1:

   #### Stack-specific `.claudeignore` patterns

   | Stack trigger | Patterns to add |
   |---|---|
   | Node / npm | `package-lock.json` |
   | Yarn | `yarn.lock` |
   | pnpm | `pnpm-lock.yaml` |
   | .NET | `*.Designer.cs`, `*.g.cs`, `**/wwwroot/lib/` |
   | Python | `poetry.lock`, `Pipfile.lock` |
   | Go | `go.sum` |
   | Rust | `Cargo.lock` |
   | Angular | `.angular/` |
   | Next.js | `.next/` |

   Add patterns under a `# <stack> files` comment section. Only add sections for stacks detected in the project. Example output for an Angular + .NET project:

   ```
   # .claudeignore — Files tracked by git but not useful for Claude's context.
   # Claude already respects .gitignore, so only list tracked files here.

   # ── Binary & media assets ──
   *.png
   *.jpg
   ...

   # ── .NET generated files ──
   *.Designer.cs
   *.g.cs
   **/wwwroot/lib/

   # ── Node / Angular files ──
   package-lock.json
   .angular/
   ```

5c. **Configure auto-compact** (from question 7):
   - If disabled: set the setting in `~/.claude/settings.json`. Ensure the directory
     exists, as its own Bash call:
     ```bash
     mkdir -p ~/.claude
     ```
     If `~/.claude/settings.json` exists, update it in place with two standalone Bash
     calls (never compound with `&&` — shell-rules):
     ```bash
     jq '.autoCompactEnabled = false | del(.env.CLAUDE_AUTOCOMPACT_PCT_OVERRIDE) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp
     ```
     The `jq` call and the `mv` call are separate Bash calls (no pipe, no `&&`) — this
     is a single non-compound command whose result the agent inspects before proceeding.
     `>` creates `~/.claude/settings.json.tmp` even when `jq` fails, so before running
     `mv`, verify the `jq` call exited 0 and that `~/.claude/settings.json.tmp` is
     valid non-empty JSON. Only run `mv` if that check passes; otherwise stop and
     report the failure instead of proceeding — an unconditional `mv` would silently
     clobber the user's real settings file with a broken/empty one.
     ```bash
     mv ~/.claude/settings.json.tmp ~/.claude/settings.json
     ```
     If `~/.claude/settings.json` does not exist, use the `Write` tool to create
     `~/.claude/settings.json.tmp` (the same `.tmp` intermediate as the existing-file
     path above, not the final file) with `{"autoCompactEnabled": false}` as its
     content, then run the same `mv ~/.claude/settings.json.tmp ~/.claude/settings.json`
     Bash call shown above (already allowlisted in this skill's frontmatter
     `allowed-tools`, so no permission change needed) to move it into place. This
     mirrors the jq+mv two-step pattern used for the existing-file case just above —
     Write-then-mv instead of jq-then-mv, since there's no existing file for jq to
     transform — and avoids a direct Write to the live `~/.claude/settings.json`
     path, which the flow plugin's own `guard-main-worktree.sh` hook now blocks as a
     self-protection measure for hook-config paths outside the repo (#1072).
   - If enabled (re-enable): remove the setting with two standalone Bash calls (the
     file necessarily already exists on this path — it was written by the "If disabled"
     branch above on an earlier run):
     ```bash
     jq 'del(.autoCompactEnabled) | del(.env.CLAUDE_AUTOCOMPACT_PCT_OVERRIDE) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp
     ```
     The `jq` call and the `mv` call are separate Bash calls (no pipe, no `&&`) — this
     is a single non-compound command whose result the agent inspects before proceeding.
     `>` creates `~/.claude/settings.json.tmp` even when `jq` fails, so before running
     `mv`, verify the `jq` call exited 0 and that `~/.claude/settings.json.tmp` is
     valid non-empty JSON. Only run `mv` if that check passes; otherwise stop and
     report the failure instead of proceeding — an unconditional `mv` would silently
     clobber the user's real settings file with a broken/empty one.
     ```bash
     mv ~/.claude/settings.json.tmp ~/.claude/settings.json
     ```
   Both paths also delete any `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` env key left behind by earlier versions of this skill — that override makes compaction trigger at 1% of context *used*, i.e. constantly (#725).
   This writes to `~/.claude/settings.json` (user-level Claude Code settings).

5c-bis. **Pin subagents to 200K context** (from question 7b):
   - If enabled (pin subagents): merge the env var into `~/.claude/settings.json`.
     Ensure the directory exists, as its own Bash call:
     ```bash
     mkdir -p ~/.claude
     ```
     If `~/.claude/settings.json` exists, update it in place with two standalone Bash
     calls:
     ```bash
     jq '. * {"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}' ~/.claude/settings.json > ~/.claude/settings.json.tmp
     ```
     The `jq` call and the `mv` call are separate Bash calls (no pipe, no `&&`) — this
     is a single non-compound command whose result the agent inspects before proceeding.
     `>` creates `~/.claude/settings.json.tmp` even when `jq` fails, so before running
     `mv`, verify the `jq` call exited 0 and that `~/.claude/settings.json.tmp` is
     valid non-empty JSON. Only run `mv` if that check passes; otherwise stop and
     report the failure instead of proceeding — an unconditional `mv` would silently
     clobber the user's real settings file with a broken/empty one.
     ```bash
     mv ~/.claude/settings.json.tmp ~/.claude/settings.json
     ```
     If `~/.claude/settings.json` does not exist, use the `Write` tool to create
     `~/.claude/settings.json.tmp` (the same `.tmp` intermediate as the existing-file
     path above, not the final file) with
     `{"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}` as its content, then
     run the same `mv ~/.claude/settings.json.tmp ~/.claude/settings.json` Bash call
     shown above (already allowlisted in this skill's frontmatter `allowed-tools`, so
     no permission change needed) to move it into place. This mirrors the jq+mv
     two-step pattern used for the existing-file case just above — Write-then-mv
     instead of jq-then-mv, since there's no existing file for jq to transform — and
     avoids a direct Write to the live `~/.claude/settings.json` path, which the flow
     plugin's own `guard-main-worktree.sh` hook now blocks as a self-protection
     measure for hook-config paths outside the repo (#1072).
   - If disabled (unpin): remove the env var key with two standalone Bash calls:
     ```bash
     jq 'del(.env.CLAUDE_CODE_SUBAGENT_MODEL) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp
     ```
     The `jq` call and the `mv` call are separate Bash calls (no pipe, no `&&`) — this
     is a single non-compound command whose result the agent inspects before proceeding.
     `>` creates `~/.claude/settings.json.tmp` even when `jq` fails, so before running
     `mv`, verify the `jq` call exited 0 and that `~/.claude/settings.json.tmp` is
     valid non-empty JSON. Only run `mv` if that check passes; otherwise stop and
     report the failure instead of proceeding — an unconditional `mv` would silently
     clobber the user's real settings file with a broken/empty one.
     ```bash
     mv ~/.claude/settings.json.tmp ~/.claude/settings.json
     ```
   This writes to `~/.claude/settings.json` (user-level Claude Code settings). Takes effect on **new** sessions only — remind the user to restart if the current session is on a `[1m]` model.

5d. **Generate CI/CD pipeline** (from question 8, only if user selected Yes — on a delta run where question 8 is carried, `ci.yml` is not regenerated and this step is skipped):

   **GitHub Actions** — write `.github/workflows/ci.yml`:

   ```yaml
   name: CI
   on:
     push:
       branches: [main]
     pull_request:
       branches: [main]
   jobs:
     ci:
       runs-on: ubuntu-latest
       steps:
         - uses: actions/checkout@v4
         # Setup + cache for detected stack
         # Install deps → Lint → Build → Test
         # Upload coverage artifact
   ```

   Compose the `steps` array based on the detected stack from question 1:

   **Node-based stacks** (Angular, React, Next.js, Vue):
   - `actions/setup-node@v4` with `node-version` from `engines.node` or `"20"`, and `cache` set to the detected package manager (npm/yarn/pnpm)
   - Install: `npm ci` (or `yarn install --frozen-lockfile` / `pnpm install --frozen-lockfile`)
   - Lint step, Build step, Test step from the stack-to-CI mapping table (question 8 in `phases/runtime-ci-sandbox-questions.md`)
   - `actions/upload-artifact@v4` for coverage output

   **.NET stacks**:
   - `actions/setup-dotnet@v4` with `dotnet-version` extracted from stack token
   - `dotnet restore`
   - Lint, Build, Test from the mapping table
   - `actions/upload-artifact@v4` for coverage output

   **Go**:
   - `actions/setup-go@v5` with `go-version-file: 'go.mod'` (enables built-in caching)
   - Lint: `golangci/golangci-lint-action@v6`
   - Build, Test from the mapping table
   - `actions/upload-artifact@v4` for coverage output

   **Python**:
   - `actions/setup-python@v5` with `python-version` from `pyproject.toml` or `"3.12"`
   - `pip install -e ".[dev]"` (or `pip install -r requirements.txt`)
   - Lint, Test from the mapping table (no build step)
   - `actions/upload-artifact@v4` for coverage output

   **Rust**:
   - `dtolnay/rust-toolchain@stable` (or version from `rust-toolchain.toml`)
   - `Swatinem/rust-cache@v2`
   - Lint, Build, Test from the mapping table
   - `actions/upload-artifact@v4` for coverage output

   **Monorepo strategies**:
   - **Same stack family** (all projects share same stack type): Use a matrix job with `working-directory` per project
   - **Mixed stacks** (projects have different stack types): Generate separate named jobs per project (e.g., `ci-api`, `ci-web-client`)
   - **Path-based trigger filters**: Add `paths` filters under `push`/`pull_request` scoped to each project's directory, so CI only runs for changed projects

   After writing the file, create the parent directory if needed (`mkdir -p .github/workflows` for GitHub Actions).

5e. **Generate `.cenci/Dockerfile`** (from question 9, only if the effective `sandbox.enabled` is `true` — the user's Yes to question 9 this run, or on a delta run the carried existing `sandbox.enabled: true`):

   **`BASE_VERSION` is always `latest`** — there is no resolution step. `buildBase` (`watch/internal/sandbox/launcher/engine.go:337-349`) only ever tags the base image `cenci-sandbox-base:<12-hex content hash>` and `cenci-sandbox-base:latest`; no semver tag exists in any environment, so the ARG default can only ever be the `latest` alias. `cenci sandbox build` always overrides it at build time via `--build-arg BASE_VERSION=...` (`engine.go:372,392`), so the default is never load-bearing for that path — it only matters for a bare manual `docker build`, where `latest` is exactly the recipe `sandbox/README.md` documents. An empty default must never be written instead: Docker's `InvalidDefaultArgInFrom` lint check flags any `ARG` used in a `FROM` whose default resolves to an empty or invalid image reference, and it evaluates the file statically regardless of a build-time override — so the literal `latest` (never empty, never unresolved) is what step 5e always writes.

   **Generated file format** — always emit the ARG/FROM pair below, **never** a literal `FROM cenci-sandbox-base:<version>`. `cenci sandbox build` always passes `--build-arg BASE_VERSION=...` at build time; a literal `FROM` would silently drift from what's actually built:

   ```dockerfile
   # cenci:managed-begin
   ARG BASE_VERSION=latest
   FROM cenci-sandbox-base:${BASE_VERSION}

   SHELL ["/bin/bash", "-o", "pipefail", "-c"]

   # cenci:fragment-begin <fragment 1 name>
   <selected fragment 1 content, from sandbox/fragments/*.dockerfile>
   # cenci:fragment-end <fragment 1 name>

   # cenci:fragment-begin <fragment 2 name>
   <selected fragment 2 content>
   # cenci:fragment-end <fragment 2 name>
   ...
   # cenci:managed-end
   ```

   **Per-fragment markers**: wrap each fragment's content in a `# cenci:fragment-begin <name>` / `# cenci:fragment-end <name>` marker pair, placed immediately before and after that fragment's own content within the managed block (see the example above). `<name>` is the fragment file's basename without `.dockerfile` (e.g. `sandbox/fragments/docker.dockerfile` → `docker`). This lets `watch/internal/sandbox/launcher`'s fragment-drift detector (#1048) identify exactly which installed fragment a block of content came from; an already-generated block with no per-fragment markers still gets detected, via that detector's legacy banner-line fallback.

   **Fragment concatenation order** (when multiple fragments apply, e.g. a monorepo union): **dotnet → node → playwright → go → python → rust → docker → azure**, regardless of the order projects were discovered in. Node is mandatory; the remaining fragments are stack-selected (docker and azure are config-selected — see the Docker and Azure rules of the Stack-to-fragment mapping table under question 9 in `phases/runtime-ci-sandbox-questions.md`). Concatenate the selected `sandbox/fragments/*.dockerfile` file contents in that fixed order, each wrapped in its own per-fragment marker pair, applying the **.NET version substitution** from that mapping table to the dotnet fragment only — every other fragment is included verbatim. Deduplicate — each fragment appears at most once even when multiple monorepo projects map to the same fragment.

   **Merge-safe regeneration**: the whole block above (from `# cenci:managed-begin` through `# cenci:managed-end` inclusive) is the managed block.
   - **File doesn't exist**: create `.cenci/` (`mkdir -p .cenci`) and write the managed block as the full file content.
   - **Effective sandbox flags, per ID**: the fragment selection uses the effective `sandbox.enabled`, `sandbox.dind` and `sandbox.azure` values, decided per question ID. An ID in `delta.carried` reads the existing config value; an ID in `delta.ask` this run (its key was absent and it was never offered, e.g. a config with `sandbox.enabled: true` that predates questions 9b/9c asks `sandbox.dind` and `sandbox.azure`) uses the answer just collected, so a just-answered Yes is consumed by this same generation. A Dockerfile is generated or refreshed whenever the effective `sandbox.enabled` is `true`, whether it was carried or just answered. The refresh itself never asks a question.
   - **File exists with both markers present**: replace only the text from `# cenci:managed-begin` through `# cenci:managed-end` (inclusive) with the freshly generated block. Preserve everything before the begin marker and everything after the end marker exactly as-is — this is where a team can hand-append their own `RUN` steps across re-runs. On a delta run with valid `# cenci:managed-begin` / `# cenci:managed-end` markers, refresh the managed block silently — no Q9/Q9b/Q9c prompt — and leave every byte outside the markers unchanged. Report the block diff with `git -C <worktree-path> diff -- .cenci/Dockerfile`. Only an ID that is in `delta.ask` (key absent and never offered) puts a Q9/Q9b/Q9c question to the user on a delta run; the markers never cause one.
   - **File exists with no markers** (e.g. a hand-authored legacy `.cenci/Dockerfile`): do not silently overwrite it. On a delta run, a `.cenci/Dockerfile` with no markers or malformed markers is left untouched and reported — no prompt. On an `--all` or `full` run, a `.cenci/Dockerfile` with no markers or malformed markers gets the Overwrite/Skip/Show prompt. It reuses the conflict-check UX already used for CI/CD generation (question 8 / step 5d) — same three options, same behavior:
     "Found existing `.cenci/Dockerfile` without cenci's managed markers. What would you like to do?"
     Options: "Overwrite — wrap it in managed markers and replace with the generated block", "Skip — keep the existing file, still record `sandbox` in config.json", "Show existing — display the current file contents"
     - If Skip: still record `sandbox` in config.json, don't write the file.
     - If Show existing: read and display the file, then re-ask Overwrite/Skip.
   - **File exists with malformed markers** (exactly one of `# cenci:managed-begin` / `# cenci:managed-end` present, markers out of order, or duplicate marker pairs): do **not** attempt a partial text replace — a malformed marker pair cannot be trusted to safely bound the managed block, and a replacement span is never guessed. On a delta run it is left untouched and reported exactly like the "no markers" case above (no prompt); on an `--all` or `full` run it takes the same Overwrite/Skip/Show prompt — same prompt text, same three options, same behavior.

   **Monorepo**: fragments are the mandatory Node runtime fragment plus the deduplicated union described in the Stack-to-fragment mapping table under question 9 in `phases/runtime-ci-sandbox-questions.md`, concatenated in the dotnet → node → playwright → go → python → rust → docker → azure order above — one `.cenci/Dockerfile` for the whole repo, not one per project.

   **Committed, not ignored**: `.cenci/Dockerfile` is committed to the repo. Do **not** add `.cenci/` or `.cenci/Dockerfile` to `.gitignore` — the whole point is a team-shared, reviewed Dockerfile that the launcher's per-repo image selection (see `sandbox/README.md`) builds identically for every teammate.

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
- `sandbox.enabled` — `true` if the user opted in to question 9; omit the key entirely if declined (same pattern as `cicd` — never write `enabled: false`). The script also deletes a legacy `sandbox.baseVersion` it finds on the existing config — that key is retired (see step 5e above).
- `sandbox.dind` — `true` if the user opted in to question 9b; omit the key entirely if declined (never write `dind: false`)
- `sandbox.azure` — `true` if the user opted in to question 9c; omit the key entirely if declined (never write `azure: false`). Read by `/cenci:configure` (to select `sandbox/fragments/azure.dockerfile`) **and** at launch by `cenci open`/`cenci audit` (`RepoAzureConfig` in `watch/internal/sandbox/launcher/azure.go`), which stage the host's `~/.azure` auth files read-only only for repos that set it

Because the answers are independent, `sandbox` is written whenever **any** of them is Yes:
- Q9=Yes, 9b=No, 9c=No → `"sandbox": { "enabled": true }` (no `dind`/`azure` keys)
- Q9=No, 9b=Yes, 9c=No → `"sandbox": { "dind": true }` (no `enabled`/`azure` keys)
- Q9=No, 9b=No, 9c=Yes → `"sandbox": { "azure": true }` (no `enabled`/`dind` keys)
- Q9=Yes, 9b=Yes, 9c=Yes → `"sandbox": { "enabled": true, "dind": true, "azure": true }`
- All No → omit `sandbox` entirely (same pattern as `cicd`)

On re-configuration, merge into any existing `sandbox` object rather than replacing it wholesale — this preserves the sibling keys when only one answer changes (e.g. a dind-only re-config that answers 9b=No must drop only `dind` and retain an already-enabled `sandbox.enabled`/`azure`, and so on for each sibling).

> **Not the same as `.claude/settings.json`'s `sandbox.enabled`.** Step 4 above always writes `"sandbox": { "enabled": false }` into `.claude/settings.json` — that key disables **Claude Code's own host sandbox**, because the cenci-sandbox container is the security boundary instead. This `.cenci/config.json` `sandbox` field is unrelated: it's this ticket's per-repo `.cenci/Dockerfile` toggle, consumed by cenci's configure skill and by `cenci sandbox build`'s per-repo image build — not by Claude Code itself. Same field name (`sandbox.enabled`), two different files, two different consumers, two unrelated meanings. Do not conflate them when reading or writing either file.

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
