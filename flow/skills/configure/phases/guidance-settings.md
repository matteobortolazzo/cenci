# Guidance and Settings Generation

Read this file on every run, after the questions and `### Auth Verification` in `SKILL.md` (see `SKILL.md` `### Step Map`). It holds steps 0 to 5c-bis: Codex agent adapters, guidance and client adapters, `.lsp.json`, board lifecycle labels, `.claude/settings.json`, MCP server configuration, `.claudeignore`, auto-compact and the subagent pin.

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

   Read the plugin's `docs/board-labels.md` before the loop: its `## Labels` table is the one source of every label's `<color>` and `<description>`, and its "How existence is ensured" section says which labels are self-healed by the skills' own `gh label create … || true` fallbacks, by the `cenci pipeline label` CLI, or by the reconciler's Go `GHMutator.EnsureLabels` (not a skill-level fallback) — see the reconciler bullet in the plugin's `docs/board-labels.md`. If the doc cannot be read, stop and report — never guess a color or description.

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

   The boundary-safe base deny list itself, with the rationale for its word boundaries and its known residual gaps, lives in `references/settings-template.md` — read it now, before writing or healing `permissions.deny`.

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
