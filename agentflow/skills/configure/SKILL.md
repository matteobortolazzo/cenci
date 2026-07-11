---
name: configure
description: "Claude Code-only: configure the agentflow workflow plugin, Claude settings, sandbox, MCP servers, and project guidance."
compatibility: Requires Claude Code settings, plugin environment variables, and AskUserQuestion.
argument-hint: [additional context]
user-invocable: true
disable-model-invocation: true
allowed-tools: Read, Write, Edit, Bash, Glob, Grep, AskUserQuestion
---

## Task

Help the user set up this project for the agentflow plugin.

### Parse `$ARGUMENTS`

All of `$ARGUMENTS` is optional **user context** (additional instructions or focus areas).
If empty, proceed normally with defaults.

**Profile override**: If the very first token of `$ARGUMENTS` is `container` or `host` (case-insensitive), it forces the profile (see **Container Detection** below). Strip that token before treating the remainder as user context.

### Existing Config Detection

Before asking questions, check if `.claude/config.json` already exists:

- Run `test -f .claude/config.json` to detect an existing configuration.
- **If it exists**: Read and parse it. Store as `existingConfig`. This is a **re-configuration** run — each question below will show the existing value as the default, letting the user accept it or change it.
- **If it does not exist**: Set `existingConfig` to `null`. This is a **fresh** run — proceed normally.

When `existingConfig` is present, tell the user before starting questions:
"Found existing configuration. Each question will show your current setting as the default — select it to keep it unchanged."

### Platform Detection

Before asking questions, attempt to auto-detect the platform from the git remote:

Run `git remote get-url origin 2>/dev/null` and parse the result:

| Remote URL pattern | Platform | Extracted values |
|---|---|---|
| `git@github.com:OWNER/REPO.git` | github | owner, repo |
| `https://github.com/OWNER/REPO.git` | github | owner, repo |
| Unrecognized / no remote | — | Fall back to manual questions |

Strip trailing `.git` suffix from repo names.

If user context was provided, use it to steer the configuration (e.g., skip certain questions, pre-select options, focus on specific areas).

### Container Detection

agentflow supports two **profiles**:

- **host** (default) — Claude Code runs on the host with its own sandbox (bubblewrap, `allowedDomains`, Bash allowlists). This is the historical behavior and is unchanged.
- **container** — Claude Code runs inside `dev-sandbox`'s `agent-sand` container with `--dangerously-skip-permissions`. Here the **container is the security boundary**, so the host-sandbox machinery is redundant friction: nested bubblewrap can fail, SSH-vs-HTTPS push workarounds are pointless, and permission auto-fix is moot (Claude Code ignores `permissions.allow/deny` under skip-permissions).

Determine `detectedProfile` using this precedence (stop at the first match):

1. **Explicit override**: If the **first token** of `$ARGUMENTS` is `container` or `host` (case-insensitive), that wins. Set `detectedProfile` accordingly and **strip that token** from `$ARGUMENTS` before parsing the rest as user context.
2. **`AGENT_SAND` env var** (works for both Docker and Podman): run `echo "${AGENT_SAND:-}"` as its own Bash call. If it prints `1` → `container`.
3. **Docker fallback**: run `test -f /.dockerenv` as its own Bash call. Exit 0 → `container`.
4. **Default**: `host`.

Run the detection checks as **separate** Bash calls (per `agentflow:shell-rules` — never compound them).

Auto-detection is **informational, not a gate** — do not use `AskUserQuestion` to confirm it (it's reliable, and gating would nag on every in-container run). Instead:

- When `detectedProfile = "container"` (whether by env/`/.dockerenv` or the `container` override), emit an informational message: "Detected the `agent-sand` container — using the **container profile**. The container is the security boundary, so the host sandbox, Bash allowlists, and permission auto-fix are skipped."
- On a **profile transition** during re-config (`existingConfig.profile` differs from `detectedProfile`, treating an absent `profile` field as `host`), also note the switch, e.g.: "Switching profile: **host → container** — the existing sandbox block will be replaced with `{ \"enabled\": false }` (your `permissions.allow`/`deny` are preserved)." or the reverse.

**Default values from existing config**: When `existingConfig` is not null, each question below MUST present the existing value as the pre-selected default (list it first, marked "(current)"). The user can accept with one click or change it. New fields not in `existingConfig` (e.g., `lspServers` when upgrading from a pre-LSP config) have no default and are asked normally.

| Question | `existingConfig` field | Default when field exists |
|---|---|---|
| — (auto-detected, not asked) | `profile` | Auto-detected each run; on re-config the existing value is shown as "(current)" but auto-detection wins |
| 1. Tech stack | `stack` | Pre-fill with formatted stack |
| 2. Project structure | `isMonorepo` | Pre-select based on existing value |
| 3. Branching strategy | `branchPattern` | Pre-fill with existing pattern |
| 4. Sandboxing | `sandboxEnabled` | Pre-select Yes/No |
| 5. MCP Servers | `mcpServers` | Pre-select servers where value is `true` |
| 5b. Pencil design | `pencil` | Pre-select based on `pencil.enabled`; if field absent, ask normally |
| 6. LSP Servers | `lspServers` | Pre-select servers where value is `true`; if field absent, ask normally |
| 7. Auto-compact | `autoCompactDisabled` | Pre-select Yes/No |
| 7b. Pin subagents to 200K | `pinSubagents200K` | Pre-select Yes/No |
| 8. CI/CD pipeline | `cicd` | Pre-select Yes/No based on `cicd.enabled` |

Ask these questions one at a time using the AskUserQuestion tool when possible:

1. **Tech stack**: "What's your tech stack?" (capture for stack pack selection)
   - Backend framework + version
   - Frontend framework + version
   - Test frameworks
   - Any other key technologies

2. **Project structure**: "Is this a monorepo or single project?"

**If monorepo**, continue with Steps 2a and 2b below. Otherwise skip to question 3.

#### Step 2a — Auto-detect projects

Scan the repo for project directories using these strategies (try all, deduplicate):

1. **Node workspaces**: Read `package.json` `workspaces` field and `pnpm-workspace.yaml` `packages` field
2. **Lerna**: Read `lerna.json` `packages` field
3. **.NET solutions**: Find `*.sln` files and parse `Project(...)` references for `.csproj` paths
4. **Convention directories**: Scan `packages/*/`, `apps/*/`, `projects/*/`, `src/*/` for directories containing `package.json` or `*.csproj`

For each discovered project, detect:
- **Path**: relative directory (e.g., `packages/api`)
- **Stack**: auto-detect from dependencies:
  - `@angular/core` in `package.json` → Angular
  - `react` in `package.json` → React
  - `next` in `package.json` → Next.js
  - `vue` in `package.json` → Vue
  - `.csproj` with `Microsoft.NET.Sdk.Web` → .NET API
  - `.csproj` with `Microsoft.NET.Sdk` → .NET library
  - Fallback: read `package.json` `name` or directory name
- **Build command**: auto-detect (`dotnet build` for .NET, `npm run build` for Node, etc.)
- **Test command**: auto-detect (`dotnet test` for .NET, `npm test` for Node, etc.)

Present discovered projects for confirmation using AskUserQuestion:
"Found these projects in the monorepo:
1. `<path>` — <detected-stack>
2. `<path>` — <detected-stack>
...
Are these correct? (You can add or remove projects)"

#### Step 2b — Per-project details

For each confirmed project, ask for a **one-line description** using AskUserQuestion:
"Provide a short description for each project:"
- `<path>` (<stack>): ___

Generate a slug for each project from its directory name (e.g., `packages/api` → `api`, `apps/web-client` → `web-client`).

3. **Branching strategy**: "What's your branch naming convention?"
   - Default suggestion: `feature/<id>-<description>`

4. **Sandboxing**: "Do you want to enable sandboxing? (Recommended — provides OS-level isolation for Bash commands)"

   **Container-profile guard**: If `detectedProfile = "container"`, **skip this question entirely** — set `sandboxEnabled = false` and proceed to question 5. The container is already the security boundary, so a nested host sandbox is redundant friction. This also skips the SSH-vs-HTTPS advisory below (it lives in the "If Yes" branch, which never runs on the container profile). Do **not** prompt.

   - Default: Yes
   - If Linux/WSL2: note that `bubblewrap` and `socat` must be installed (`sudo apt install bubblewrap socat` or equivalent)
   - If Yes: recommend using HTTPS git remotes instead of SSH for push support within the sandbox. SSH connections bypass `allowedDomains` network filtering, so `git push` over SSH will fail inside the sandbox. The user can switch with: `git remote set-url origin https://github.com/<owner>/<repo>.git`
   - If No: set `sandbox.enabled: false` in the generated settings

### Dependency Detection

Before asking about MCP servers, scan the project for framework dependencies:

1. If `package.json` exists in the repo root, read `dependencies` and `devDependencies`
2. If `.csproj` files exist, read `PackageReference` entries
3. Store the detected package names for matching against the MCP catalog below

### MCP Server Catalog

| Trigger Package | Server Name | Command | Args | Env Vars | Scope |
|---|---|---|---|---|---|
| *(always available)* | context7 | `npx` | `["-y", "@upstash/context7-mcp"]` | `CONTEXT7_API_KEY` | plugin |
| *(Pencil editor open)* | pencil | (connected via editor) | — | — | editor |
| `@angular/core` | angular | `npx` | `["-y", "@angular/cli", "mcp"]` | — | project |
| `primeng` | primeng | `npx` | `["-y", "@primeng/mcp"]` | — | project |

**Scope:**
- **plugin**: Already defined in agentflow's `.mcp.json`. Enable by setting `disabled: false`.
- **project**: Add to the project's root `.mcp.json`.

### LSP Server Catalog

| Trigger | Server Name | Command | Args | Extension Map | Install Command |
|---|---|---|---|---|---|
| `typescript` or `@angular/core` or `react` or `next` or `vue` in package.json | typescript | `typescript-language-server` | `["--stdio"]` | `{".ts": "typescript", ".tsx": "typescriptreact", ".js": "javascript", ".jsx": "javascriptreact"}` | `npm install -g typescript-language-server typescript` |
| `*.py` files or `pyproject.toml` or `requirements.txt` | pyright | `pyright-langserver` | `["--stdio"]` | `{".py": "python"}` | `pip install pyright` or `npm install -g pyright` |
| `Cargo.toml` present | rust-analyzer | `rust-analyzer` | `[]` | `{".rs": "rust"}` | See rust-analyzer docs |
| `*.csproj` present | csharp-ls | `csharp-ls` | `[]` | `{".cs": "csharp"}` | `dotnet tool install --global csharp-ls` |
| `go.mod` present | gopls | `gopls` | `["serve"]` | `{".go": "go"}` | `go install golang.org/x/tools/gopls@latest` |

5. **MCP Servers**: Match detected dependencies against the MCP catalog above. Build a suggestion list:
   - Always include **Context7** (general-purpose docs lookup)
   - Add each MCP whose trigger package was found in the dependency scan

   Present using AskUserQuestion with multiSelect=true:

   "Based on your project dependencies, these MCP servers can enhance AI assistance.
    Which would you like to enable?"

   Options (only show those whose trigger was detected, plus Context7 always):
   - "Context7 — Live documentation lookup for any library (requires free API key from context7.com/dashboard)"
   - "Angular — Official Angular AI tutor, best practices, and documentation search"
   - "PrimeNG — Component documentation, props, events, theming, and examples"

   If only Context7 is available (no framework-specific MCPs detected), still present it:
   "Do you want to enable Context7 for live documentation lookup?
    (Requires a free API key from context7.com/dashboard)"

### Pencil Design Workflows

**Condition**: Only ask question 5b when a frontend framework is detected in the stack from question 1. Frontend frameworks include: Angular, React, Next.js, Vue, Svelte, or any UI framework.

If no frontend framework is detected, skip this section entirely (do not set `pencil` in config).

5b. **Pencil design workflows**: Present using AskUserQuestion:

   "Your project includes `<detected-frontend-framework>`. Do you want to enable Pencil design workflows?
    (Visual designs, auto-generated design specs with component mappings and tokens.
    Requires the Pencil editor.)"

   Options: "Yes — enable Pencil design workflows", "No — skip"

   **If Yes AND monorepo with multiple frontend projects** (i.e., `isMonorepo` is true and more than one project in the `projects` array has a frontend stack):

   "Should frontend projects share one design file, or have separate design files?"

   Options: "Shared (single `designs/` at repo root)", "Separate (per-project `designs/`)"

   - **Shared**: `pencil.designPath = "designs/"`, `pencil.shared = true`
   - **Separate**: each frontend project entry in the `projects` array gets its own `designPath` (e.g., `"<project-path>/designs/"`)

   **If Yes AND single project** (or monorepo with only one frontend project):
   - `pencil.designPath = "designs/"`, `pencil.shared` is omitted

   **After the user confirms Yes** (regardless of monorepo choice), detect `pencil interactive` support:

   Run `pencil interactive --help 2>/dev/null` and check the exit code:
   - **Succeeds (exit 0)** → Write `pencil.mode: "cli-app"` to the config. Inform the user:
     "Pencil `interactive` mode detected. Design skills will use `pencil interactive` to communicate with the Pencil editor — this is more token-efficient than the MCP server.
     For maximum token savings, you can disable the Pencil MCP server in your editor settings (Pencil → Preferences → MCP Server). agentflow uses the CLI directly and does not need the MCP server."
   - **Fails or not found** → Write `pencil.mode: "editor"` to the config. Inform the user:
     "Pencil `interactive` mode not available. Design skills will use the Pencil MCP server (requires the MCP connection to be active in your editor).
     For better token efficiency, install the `pencil` command from within the Pencil app (File → Install `pencil` command into PATH) and re-run `/agentflow:configure` — this switches to `cli-app` mode which avoids loading MCP tool schemas into every conversation."

### Playwright CLI Setup

**Condition**: Only ask this when a frontend framework is detected in the stack from question 1 AND `@playwright/test` is found in `devDependencies`.

If both conditions are met, present using AskUserQuestion:

   "Your project uses Playwright Test. Do you want to set up Playwright CLI (`@playwright/cli`) for interactive browser automation during development?
    (Screenshots, snapshots, form filling, network inspection — more token-efficient than Chrome MCP for agents.)"

   Options: "Yes — install and configure Playwright CLI", "No — skip"

   **If Yes**:
   1. Check if `playwright-cli` is already installed: `which playwright-cli 2>/dev/null`
      - **Found** → "✓ `playwright-cli` found at `<path>`"
      - **Not found** → "Run `npm i -g @playwright/cli` to install, then `playwright-cli install --skills` to set up agent skills."
   2. Set `playwrightCli: true` in `.claude/config.json`

   **If No**: Set `playwrightCli: false` in `.claude/config.json` (or omit the field)

If the conditions are not met, skip this section entirely (do not set `playwrightCli` in config).

### LSP Detection

Reuse the dependency detection results from earlier and add file-type detection to match against the LSP Server Catalog:

- `typescript`, `@angular/core`, `react`, `next`, or `vue` in `package.json` dependencies → **typescript**
- `*.py` files present, or `pyproject.toml`, or `requirements.txt` → **pyright**
- `Cargo.toml` present → **rust-analyzer**
- `*.csproj` present → **csharp-ls**
- `go.mod` present → **gopls**

If no LSP servers are detected, skip question 6 entirely.

6. **LSP Servers**: If any LSP servers were detected above, present using AskUserQuestion with multiSelect=true:

   "LSP servers provide real-time diagnostics (type errors, unused variables, dead code) during implementation. Based on your project, which would you like to enable?"

   Options (only show those whose trigger was detected):
   - "typescript — TypeScript/JavaScript type checking and diagnostics"
   - "pyright — Python type checking and diagnostics"
   - "rust-analyzer — Rust type checking and diagnostics"
   - "csharp-ls — C# type checking and diagnostics"
   - "gopls — Go type checking and diagnostics"

#### Binary Verification

For each LSP server the user selected, verify the binary is installed:

```bash
which <command>
```

- **Found**: Confirm with a checkmark: "✓ `<command>` found at `<path>`"
- **Not found**: Warn with install command: "⚠ `<command>` not found. Install with: `<install-command>`. Server will activate once installed."

Include the server in `.lsp.json` regardless — it activates once the binary is installed.

7. **Disable auto-compact**: "Do you want to disable Claude Code's auto-compact feature?
    Auto-compact compresses conversation history as the context window fills,
    which can lose important context during long sessions. (Recommended: Yes — disable it)"
   - Default: Yes
   - If Yes: merge `{"env": {"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "1"}}` into `~/.claude/settings.json` using jq (create the file if it doesn't exist). This sets compaction to trigger at 1% — effectively manual-only.
   - If No: remove the `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` key from the `env` object in `~/.claude/settings.json` (if present)

7b. **Pin subagents to 200K context**: "Do you want to pin agentflow's subagents to a 200K-context
    model? agentflow delegates reviews to subagents, and on 1M-context sessions that delegation can be
    gated — every subagent inherits the session's 1M flag but not its extra-usage entitlement, so
    reviews fail with 'Usage credits required for 1M context' (Claude Code bug #51060). Pinning
    subagents to Sonnet 200K keeps reviews working while your main session keeps its 1M context.
    (Recommended: Yes if you run a 1M-context session; No otherwise — see the tiering caveat below)"
   - Default: Yes when the session plausibly runs a 1M model; No otherwise
   - If Yes: merge `{"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}` into `~/.claude/settings.json` using jq (create the file if it doesn't exist). This runs all `Task` subagents on Sonnet 200K regardless of the main session model. (Pin Sonnet, not Opus — Opus is auto-upgraded to 1M on Max/Team/Enterprise plans and would re-trigger the gate.)
   - If No: remove the `CLAUDE_CODE_SUBAGENT_MODEL` key from the `env` object in `~/.claude/settings.json` (if present)
   - **Tiering caveat (state regardless of answer)**: the pin overrides every agent's `model:` frontmatter — agentflow's model tiering (opus planner/security-reviewer, haiku context-gatherer/structure-analyzer/lessons-collector) is flattened onto the pinned model while it is set. On a standard 200K session the 1M gate never fires, so answer No there to keep the tiering active.
   - **Caveat (state regardless of answer)**: this only affects **new** sessions — restart after configuring. If subagent reviews still fail with the 1M gate even after pinning (the pin didn't strip `[1m]`), run `/model sonnet` for the current session, which always yields 200K.

8. **CI/CD pipeline**: "Do you want to generate a CI/CD pipeline?"
   - Options: "Yes — generate a CI workflow", "No — skip"
   - Default: No
   - Platform: GitHub Actions

   **Conflict check**: If the user selects Yes, scan for existing CI configuration:
   - List `.github/workflows/*.yml` and `.github/workflows/*.yaml` (e.g., `ls .github/workflows/*.yml .github/workflows/*.yaml 2>/dev/null`)

   If any files are found, present them and ask:
   - **Single file**: "Found existing CI configuration at `<path>`. What would you like to do?"
   - **Multiple files**: "Found existing CI workflows:\n  - `<path1>`\n  - `<path2>`\n  ...\nWhat would you like to do?"

   Options: "Overwrite — generate `ci.yml` (existing files are not deleted)", "Skip — keep existing files", "Show existing — display the current file contents"
   - If Skip: still record `cicd` in config.json, don't write the file
   - If Show existing: read and display each file, then re-ask Overwrite/Skip

   **Stack-to-CI mapping**: Use the detected stack from question 1 to select the appropriate lint, build, and test commands:

   | Stack | Lint | Build | Test |
   |---|---|---|---|
   | `dotnet*` | `dotnet format --verify-no-changes` | `dotnet build --no-restore` | `dotnet test --no-build --collect:"XPlat Code Coverage"` |
   | `go` | `golangci-lint run ./...` | `go build ./...` | `go test ./... -coverprofile=coverage.out` |
   | `python` | `ruff check .` | *(none)* | `pytest --cov=. --cov-report=xml` |
   | `rust` | `cargo clippy -- -D warnings` | `cargo build` | `cargo test` |
   | `angular*` | `ng lint` | `ng build --configuration production` | `ng test --watch=false --code-coverage` |
   | `react` / `next` | `npx eslint .` | `npm run build` | `npm test -- --coverage --watchAll=false` |
   | `vue` | `npx eslint .` | `npm run build` | `npm run test:unit -- --coverage` |

   **Package manager detection** (for Node-based stacks):
   - `pnpm-lock.yaml` → pnpm
   - `yarn.lock` → yarn
   - `package-lock.json` → npm
   - .NET → NuGet cache on `**/*.csproj`
   - Go → built-in cache in `actions/setup-go@v5`

   **Version pinning**:
   - Node: `engines.node` from `package.json`, fallback `"20"`
   - .NET: extract from stack token (`dotnet10` → `"10.x"`)
   - Go: first line of `go.mod`
   - Python: `python-requires` from `pyproject.toml`, fallback `"3.12"`
   - Rust: `rust-toolchain.toml` or `"stable"`

### Auth Verification

Before generating config, verify CLI authentication:

Run `gh auth status` and check it returns authenticated. If not, instruct the user to run `gh auth login` first.

After gathering answers:

1. **Detect existing CLAUDE.md location**:
   - Check if `CLAUDE.md` exists at the repo root: `test -f CLAUDE.md`
   - Check if `.claude/CLAUDE.md` exists: `test -f .claude/CLAUDE.md`
   - **If both exist**: Ask the user: "Found CLAUDE.md at both the repo root and `.claude/CLAUDE.md`. Which location do you want to keep?" Options: "Root (CLAUDE.md)", ".claude/ directory (.claude/CLAUDE.md)". Delete the unchosen file after updating the kept one.
   - **If only root exists**: Ask the user: "Found existing `CLAUDE.md` at the repo root. Do you want to keep it there or move it to `.claude/CLAUDE.md`?" Options: "Keep at root", "Move to .claude/". If moving, delete the root file after creating `.claude/CLAUDE.md`.
   - **If only `.claude/CLAUDE.md` exists**: Keep it in place.
   - **If neither exists**: Create at `.claude/CLAUDE.md` (default).

   **Single-project**: Update the chosen file using the template at `${CLAUDE_PLUGIN_ROOT}/templates/claude-md-root.md` — customize with stack. If the file already has content, merge the template sections into it rather than overwriting (preserve user-added content).

   **Monorepo**: Update the chosen file using the template at `${CLAUDE_PLUGIN_ROOT}/templates/claude-md-root-monorepo.md` instead — customize with project count and populate the Projects table from the discovered projects. If the file already has content, merge the template sections into it rather than overwriting (preserve user-added content).

1b. **Generate `.lsp.json`** (if any LSP servers were selected in question 9):

   Write `.lsp.json` **in the project root** (not `${CLAUDE_PLUGIN_ROOT}`) with entries for selected servers only, using the command, args, and extension map from the LSP Server Catalog. Claude Code only reads `.lsp.json` from the project root — plugin-scoped LSP config is not supported. If `.lsp.json` already exists in the project root, merge new entries into it (preserve existing entries). Example:

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

   `.claude/rules/` is reserved for files explicitly `@`-imported by `CLAUDE.md` (auto-loaded at session start). Do NOT deploy reference docs there. Today, configure does not write any files into `.claude/rules/` — leave it absent unless the user opts into auto-loaded rules later.

   **Backward compatibility**: Do NOT delete or migrate any existing `.claude/rules/lessons-learned.md`, `.claude/rules/lessons-learned-<slug>.md`, or `.claude/rules/git-workflow.md` files. Skills and agents continue to read them as legacy fallback if present.

   Do NOT deploy `testing.md` or `security.md` — testing rules load on-demand via the `testing` skill, and security rules are distributed across root CLAUDE.md (universal), per-project CLAUDE.md (stack-specific), and stack skills.

**Monorepo-only additional files:**

3a. For each project, create `<project-path>/CLAUDE.md` using the template at `${CLAUDE_PLUGIN_ROOT}/templates/claude-md-project.md` — customize with:
   - `<project-name>`: the project name (from slug or user input)
   - `<stack>`: detected stack
   - `<repo-name>`: the repository name
   - `<framework + version>`: detected framework
   - `<test-framework>`: detected test framework
   - `<build-command>`: detected or user-provided build command
   - `<test-command>`: detected or user-provided test command
   - `<project-specific rules populated during configure>`: leave as a placeholder for the user to fill in later, or remove the bullet if no conventions are known yet

3b. **Create design directories** (only if Pencil was enabled in question 5b):
   - **Single project or shared monorepo** (`pencil.shared` is `true` or not a monorepo): Create `designs/` at the repo root: `mkdir -p designs/`
   - **Separate monorepo**: For each frontend project that has a `designPath`, create its directory: `mkdir -p <project-path>/designs/`

4. **Create or update `.claude/settings.json`**:

   **Container profile** (`detectedProfile = "container"`): write the minimal shape below — the host sandbox is disabled because the container is the boundary. Under `--dangerously-skip-permissions` Claude Code ignores `permissions.allow/deny`, but keep the base allow list + deny rules as defense-in-depth for the case where a user runs plain `claude` (no skip-permissions) inside the container, e.g. via `agent-sand --shell`.

   ```json
   {
     "sandbox": { "enabled": false },
     "permissions": { "allow": [ … ], "deny": [ … ] }
   }
   ```

   - **Fresh** (no existing settings.json): copy `${CLAUDE_PLUGIN_ROOT}/templates/settings.json` as the base for `permissions`, then set `sandbox` to `{ "enabled": false }` (drop any `network`, `excludedCommands`, `autoAllowBashIfSandboxed` the template may carry).
   - **Existing** settings.json (host→container re-config): read it first, then **replace the entire `sandbox` block** with `{ "enabled": false }` (drop `network`, `excludedCommands`, `autoAllowBashIfSandboxed`). **Preserve** `permissions.allow` and `permissions.deny` exactly, including user-added entries.
   - Still **append** stack-specific `Bash(...)` allow rules and enabled-MCP tool entries (per the "Append to `permissions.allow`" bullets below). Do **NOT** append `sandbox.network.allowedDomains` — there is no network block on the container profile.
   - Omit `sandbox.network`, `sandbox.excludedCommands`, and `sandbox.autoAllowBashIfSandboxed` entirely — they are meaningless when the sandbox is disabled.
   - The "base permissions MUST remain" invariant below applies unchanged: never remove or replace existing `permissions.allow` entries; only append.

   Then skip the **host** branch that follows and continue at the "Pending-plans detection" note.

   **Host profile** (`detectedProfile = "host"`, i.e. the default / absent `profile`): If the file already exists, read it first and merge new settings into it (preserve user-added entries in `permissions.allow`, `permissions.deny`, and `sandbox.network.allowedDomains`). If the file does not exist, copy `${CLAUDE_PLUGIN_ROOT}/templates/settings.json` as the base. Then **append** to it:

   > **IMPORTANT**: All base permissions from the template (`Write`, `Edit`, `Read(~/.claude/plugins/**)`, `Read(//tmp/claude*/**)`, `Write(//tmp/claude*/**)`, `Bash(cd:*)`, `Bash(git:*)`, `Bash(gh:*)`, `SlashCommand(/goal:*)`, `SlashCommand(/loop:*)`, etc.) **MUST** remain in `permissions.allow`. Only **append** new entries — never remove or replace existing ones. When updating an **existing** `settings.json`, also ensure these base entries are present — add any that are missing (older configs predate them — e.g. `SlashCommand(/goal:*)` for the implement autopilot and `SlashCommand(/loop:*)` for `/agentflow:babysit`). The `Read(~/.claude/plugins/**)` rule lets the pipeline read its own plugin files (phase docs resolve to `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/skills/…`) without prompting — it is deliberately scoped to `plugins/` so subagents cannot read session transcripts or global config under `~/.claude/`; the `//tmp/claude*/**` rules cover the `shell-rules` heredoc temp-file pattern and the session scratchpad.

   **Append to `sandbox.network.allowedDomains`:**
   - GitHub domains (`github.com`, `api.github.com`)
   - Stack-specific domains (e.g., `nuget.org` for .NET, `registry.npmjs.org` for Angular/Node, `proxy.golang.org` and `sum.golang.org` for Go)

   **Append to `permissions.allow`:**
   - Stack-specific rules (e.g., `Bash(dotnet:*)` for .NET, `Bash(ng:*)` for Angular, `Bash(go:*)` for Go)
   - For each enabled MCP, add its tool permissions. Look up the server's available tools
     and add entries in the format `mcp__<server-name>__<tool-name>` (for project-scoped)
     or `mcp__plugin_agentflow_<server-name>__<tool-name>` (for plugin-scoped).
     Known tools:
     - **Context7**: `mcp__plugin_agentflow_context7__resolve-library-id`, `mcp__plugin_agentflow_context7__query-docs`
     - **Pencil** (only if `pencil.enabled` is `true`): `mcp__pencil__*` (auto-allow all Pencil editor MCP tools — only relevant when Pencil editor is open)
     - **Angular**: `mcp__angular__:*` (auto-allow all Angular CLI MCP tools)
     - **PrimeNG**: `mcp__primeng__:*` (auto-allow all PrimeNG MCP tools)

   **Other settings:**
   - If user declined sandboxing: set `sandbox.enabled: false`

   **Pending-plans detection** — no per-project setup required. The pending-plans
   SessionStart hook is shipped plugin-side (`${CLAUDE_PLUGIN_ROOT}/hooks/scripts/check-pending-plans.sh`,
   registered in `agentflow/hooks/hooks.json`) and runs automatically wherever agentflow is
   enabled. Do **not** add a SessionStart entry to `.claude/settings.json` or copy any
   script into `.claude/hooks/`.

   **Legacy cleanup** — heal projects configured by an older agentflow that installed the
   hook per-project (a fragile cwd-relative path that errored in worktrees and
   subdirectories):
   1. If `.claude/settings.json` has a `hooks.SessionStart` hook whose `command` is
      `.claude/hooks/check-pending-plans.sh`, remove that hook entry. If its enclosing
      block's `hooks` array becomes empty, remove the block too; if `SessionStart`
      becomes empty, remove it. Preserve every other SessionStart entry untouched.
   2. Delete the orphaned script if present: `rm -f .claude/hooks/check-pending-plans.sh`.
      Then remove the directory only if it is now empty: `rmdir .claude/hooks 2>/dev/null || true`
      (the `|| true` keeps it non-fatal when the dir is absent or still holds other hooks;
      run as its own Bash call, never compounded with a `cd` — see `agentflow:shell-rules`).

### MCP Server Configuration

For each MCP selected in question 5:

**Plugin-scoped (Context7):**
- In agentflow's `.mcp.json` (`${CLAUDE_PLUGIN_ROOT}/.mcp.json`), set `mcpServers.context7.disabled` to `false`
- Note to user: "Set CONTEXT7_API_KEY in your shell environment (free key from context7.com/dashboard)"

**Project-scoped (Angular, PrimeNG, etc.):**
- Only create or modify the project's root `.mcp.json` if at least one project-scoped MCP server was selected. Never create an empty `.mcp.json`.
- Create or update the project's root `.mcp.json`
- Add entries from the catalog, e.g.:
  ```json
  {
    "mcpServers": {
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
- If the file already exists, merge into the existing `mcpServers` object — never overwrite existing entries

5. Update `.gitignore`:
   - Add `.worktrees/` if not present
   - Add `.plans/` if not present (plan files are ephemeral, session-specific)
   - **Container profile** (`detectedProfile = "container"`): `sandboxEnabled` is `false`, so the "If sandbox is enabled" block below is skipped — do **not** add sandbox artifact entries. (If a prior host-profile run already appended them, leave them in place — they are harmless and removing them would violate the append-only rule.)
   - **If sandbox is enabled**: Add sandbox artifact entries if not already present. The sandbox exposes git internals, shell configs, and tool configs as visible entries in the working directory. Add these entries under a `# Claude Code sandbox artifacts` comment:
     ```
     # Claude Code sandbox artifacts
     # Git internals
     HEAD
     config
     hooks/
     objects/
     refs/

     # Shell configs
     .bash_profile
     .bashrc
     .zshrc
     .profile
     .zprofile

     # Tool configs
     .gitconfig
     .gitmodules
     .ripgreprc

     # IDE/tool configs
     .idea/
     .vscode/
     ```
   - Check each entry individually before adding — skip any that are already in `.gitignore`
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
   - If disabled: merge the env var into `~/.claude/settings.json`:
     ```bash
     mkdir -p ~/.claude && \
     [ -f ~/.claude/settings.json ] \
       && jq '. * {"env": {"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "1"}}' ~/.claude/settings.json > ~/.claude/settings.json.tmp \
       && mv ~/.claude/settings.json.tmp ~/.claude/settings.json \
       || echo '{"env": {"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "1"}}' > ~/.claude/settings.json
     ```
   - If enabled (re-enable): remove the env var key:
     ```bash
     jq 'del(.env.CLAUDE_AUTOCOMPACT_PCT_OVERRIDE) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp \
       && mv ~/.claude/settings.json.tmp ~/.claude/settings.json
     ```
   This writes to `~/.claude/settings.json` (user-level Claude Code settings).

5c-bis. **Pin subagents to 200K context** (from question 7b):
   - If enabled (pin subagents): merge the env var into `~/.claude/settings.json`:
     ```bash
     mkdir -p ~/.claude && \
     [ -f ~/.claude/settings.json ] \
       && jq '. * {"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}' ~/.claude/settings.json > ~/.claude/settings.json.tmp \
       && mv ~/.claude/settings.json.tmp ~/.claude/settings.json \
       || echo '{"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}' > ~/.claude/settings.json
     ```
   - If disabled (unpin): remove the env var key:
     ```bash
     jq 'del(.env.CLAUDE_CODE_SUBAGENT_MODEL) | if .env == {} then del(.env) else . end' ~/.claude/settings.json > ~/.claude/settings.json.tmp \
       && mv ~/.claude/settings.json.tmp ~/.claude/settings.json
     ```
   This writes to `~/.claude/settings.json` (user-level Claude Code settings). Takes effect on **new** sessions only — remind the user to restart if the current session is on a `[1m]` model.

5d. **Generate CI/CD pipeline** (from question 8, only if user selected Yes):

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
   - Lint step, Build step, Test step from the stack-to-CI mapping table
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

6. **Write `.claude/config.json`** with their choices using **merge semantics**:

   - If `existingConfig` is not null: start from the existing object, overwrite each field with the user's answers. This preserves fields the skill doesn't manage.
   - If `existingConfig` is null: create the file fresh.
   - Always set `profile` to the auto-detected `detectedProfile` (`"host"` or `"container"`) — on re-config this overwrites any stale value, reflecting where configure actually ran. Set `sandboxEnabled` accordingly (`false` on the container profile).

```json
{
  "branchPattern": "feature/<id>-<description>",
  "stack": {
    "backend": "dotnet10",
    "frontend": "angular21",
    "testing": ["xunit", "jasmine"]
  },
  "sandboxEnabled": true,
  "profile": "host",
  "claudeMdLocation": ".claude/CLAUDE.md | CLAUDE.md",
  "mcpServers": {
    "context7": true,
    "angular": false,
    "primeng": false
  },
  "lspServers": {
    "typescript": true,
    "csharp-ls": true
  },
  "pencil": {
    "enabled": true,
    "designPath": "designs/",
    "mode": "editor"
  },
  "autoCompactDisabled": true,
  "pinSubagents200K": true,
  "agentflow": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "diffContextMode": "inline",
    "goalAutopilot": true,
    "planComment": false
  },
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  }
}
```

The `agentflow` field is optional. If present, preserve existing user values during reconfiguration. Schema:
- `compactImplementation` — `true` allows small, low-risk tickets to combine red/green/refactor into one implementer subagent turn while preserving all TDD/reporting gates. Default: `false`.
- `reviewConcurrency` — `"parallel"` runs security, code, and silent-failure reviews together; `"sequential"` runs the same reviews one after another to smooth usage limits. Default: `"parallel"`.
- `diffContextMode` — `"inline"` passes small diffs directly to reviewers; `"file"` writes the diff to `/tmp/claude/agentflow-diff.patch` and passes paths so reviewers read targeted hunks. Default: `"inline"`.
- `goalAutopilot` — `true` arms a native `/goal` completion condition at Phase 2 start (Claude Code ≥ 2.1.139) so implement phases 2–9 resume through to an open PR after a mid-phase stop; `false` opts out. Default (unset): enabled where supported, a graceful no-op on older runtimes.
- `planComment` — `true` makes implement Phase 1 also post the approved plan as a ticket comment (ticket mode only) right after marking the ticket `Planned`, for audit / off-host visibility; `.plans/` stays the executable source of truth. Default: `false` (no comment).

The `profile` field records which security model this project runs under and is **auto-detected each run** (never asked as a question). Values:
- `"host"` (default; also assumed when the field is **absent** — fully backward-compatible) — Claude Code runs on the host with its own sandbox. Configure writes the full sandbox block (or `enabled: false` if the user declined sandboxing in Q4).
- `"container"` — Claude Code runs inside the `agent-sand` container (`dev-sandbox`) with `--dangerously-skip-permissions`; the container is the security boundary. Configure skips Q4, sets `sandboxEnabled: false`, writes `sandbox: { "enabled": false }` (no `network`/`excludedCommands`/`autoAllowBashIfSandboxed`), and skips sandbox `.gitignore` artifacts.

Detection precedence: an explicit first-token override (`/agentflow:configure container` or `/agentflow:configure host`, case-insensitive, stripped before further parsing) → the `AGENT_SAND=1` env var (set by `agent-sand` for both Docker and Podman) → `/.dockerenv` (Docker fallback) → default `host`. Detection is informational, not gated. Re-config across profiles is safe: the `sandbox` block is owned entirely by configure and is rewritten every run, while `permissions.allow`/`deny` are always preserved.

Optional external usage reducer: RTK (`https://github.com/rtk-ai/rtk`) can compress shell command output before it reaches Claude Code. It is not required for agentflow and should not be installed automatically, but it is worth recommending when users are hitting usage limits from command-heavy sessions. After separate installation, `rtk init -g` enables Claude Code Bash command rewriting where supported. Built-in tools like `Read`, `Grep`, and `Glob` do not pass through RTK hooks.

The `cicd` field is only present when the user selected Yes in question 8. Schema:
- `cicd.enabled` — `true` if user opted in, omit `cicd` entirely if declined
- `cicd.platform` — `"github-actions"`

Omit `cicd` entirely when the user says No (same pattern as `pencil`).

The `pencil` field is only present when the user was asked question 5b (frontend framework detected). Schema:
- `pencil.enabled` — gating flag for all design features (`true` if user opted in, `false` if declined)
- `pencil.designPath` — where `.pen` and `DESIGN.md` files live (default: `"designs/"`)
- `pencil.mode` — Pencil connection mode: `"editor"` (default, GUI with MCP), `"headless"` (future, npm package), or `"auto"` (future, try headless then editor)
- `pencil.shared` — only present when `isMonorepo: true` and user chose shared design files (`true` for shared, omitted for separate)

Omit `pencil` entirely if no frontend framework was detected.

**Monorepo config** — when `isMonorepo` is true, add `isMonorepo` and `projects` fields:

```json
{
  "isMonorepo": true,
  "profile": "host",
  "projects": [
    {
      "slug": "api",
      "path": "packages/api",
      "name": "API",
      "description": "REST API backend",
      "stack": { "framework": "dotnet10", "testing": "xunit" },
      "buildCommand": "dotnet build",
      "testCommand": "dotnet test"
    },
    {
      "slug": "web-client",
      "path": "apps/web-client",
      "name": "Web Client",
      "description": "Angular frontend",
      "stack": { "framework": "angular21", "testing": "jasmine" },
      "buildCommand": "npm run build",
      "testCommand": "npm test",
      "designPath": "apps/web-client/designs/"
    }
  ],
  "pencil": {
    "enabled": true,
    "designPath": "designs/",
    "shared": true
  },
  "autoCompactDisabled": true,
  "pinSubagents200K": true,
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  }
}
```

Existing single-project configs (no `isMonorepo` field) work unchanged.

Only include servers in `mcpServers` that were presented as options (i.e., detected or always-available). Value is `true` if enabled, `false` if declined.

Only include servers in `lspServers` that were detected and presented in question 6. Value is `true` if enabled, `false` if declined. Omit `lspServers` entirely if no LSP servers were detected.

When migrating from an older config that has `ticketSystem`, `prSystem`, `ticketPrefix`, `adoOrg`, `adoProject`, or `adoRepo` fields, remove them during the merge.

**Monorepo `pencil` notes:**
- When `pencil.shared` is `true`: `pencil.designPath` holds the shared path (e.g., `"designs/"`). Individual projects do **not** have `designPath`.
- When `pencil.shared` is `false` (separate): `pencil.designPath` is omitted. Each frontend project in the `projects` array gets a `designPath` field (e.g., `"apps/web-client/designs/"`). Non-frontend projects do not get `designPath`.

Report what was created and suggest next steps (e.g., "Try `/agentflow:refine <ticket-id>` on a ticket").

### Board lifecycle labels

configure does **not** create labels — the skills apply them via `gh issue edit` as a ticket
moves through the workflow. Document the label set in the completion summary so the user can
mirror it as columns on their board:

| Label | Applied by | Meaning |
|---|---|---|
| `Working` | refine / implement (at start) | Actively being refined or implemented |
| `Refined` | refine | Ready for design/implementation |
| `Designed` | design | UI design spec approved |
| `Planned` | implement Phase 1 (approved plan persisted) | Approved plan on disk, ready to pick up |
| `In Review` | implement Phase 9 (at PR-open) | PR is open, under review / CI running |
| `Implemented` | babysit (on PR merge) | PR merged — done |

Lifecycle: `New → Refined → [Designed] → Planned → Working → In Review → Implemented`.

**Migration note for existing boards** (state this when re-configuring a project that predates
`In Review`): add an **`In Review`** column/label. Previously, tickets dropped straight into
`Implemented` the moment the PR was opened; now PR-open lands them in `In Review`, and
`/agentflow:babysit` promotes them to `Implemented` when the PR actually merges. Existing tickets
that carry `Implemented` from before this change but whose PRs are still open can be relabeled to
`In Review` by hand; new work follows the split automatically.

**Migration note for existing boards** (state this when re-configuring a project that predates
`Planned`): add a **`Planned`** column/label between `Designed` and `Working`. Previously, a
planning session applied `Working` at pipeline start and then stopped once the plan was approved —
so the board showed `Working` on a ticket nobody was actively working. Now a planning session
lands the ticket on `Planned` ("an approved plan exists on disk, ready to pick up"); the swap to
`Working` happens when the plan-file implementation run begins. New work follows this
automatically.
