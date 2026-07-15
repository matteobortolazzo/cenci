---
name: configure
description: "Claude Code-only: configure the cenci workflow plugin, Claude settings, MCP servers, and project guidance."
compatibility: Requires Claude Code settings, plugin environment variables, and AskUserQuestion.
argument-hint: [additional context]
user-invocable: true
disable-model-invocation: true
allowed-tools: Read, Write, Edit, Bash, Glob, Grep, AskUserQuestion
---

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

## Task

Help the user set up this project for the cenci plugin.

### Parse `$ARGUMENTS`

All of `$ARGUMENTS` is optional **user context** (additional instructions or focus areas).
If empty, proceed normally with defaults.

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

cenci runs inside `sandbox`'s cenci-sandbox container with `--dangerously-skip-permissions`. The **container is the security boundary** — there is no host profile. Claude Code's own host sandbox stays disabled, and `permissions.allow`/`deny` are kept only as defense-in-depth for plain `claude` runs inside the container (e.g. `cenci open --shell`).

Detect the container (stop at the first match; run each check as its **own** Bash call, per `cenci:shell-rules` — never compound them):

1. **`CENCI_SANDBOX` env var** (works for both Docker and Podman): run `echo "${CENCI_SANDBOX:-}"` as its own Bash call. If it prints `1` → in container.
2. **Docker fallback**: run `test -f /.dockerenv` as its own Bash call. Exit 0 → in container.

This detection is a **non-blocking advisory** — it never gates configuration and never uses `AskUserQuestion`:

- **In container**: emit an informational message: "Detected the cenci-sandbox container — the container is the security boundary. Claude Code's host sandbox stays disabled." Then continue normally.
- **Not in container**: emit an advisory and continue anyway: "cenci is designed to run inside the cenci-sandbox container (the security boundary). You appear to be running on the host — continuing, but running outside the container is unsupported." Proceed with the same container-shaped output.

**Default values from existing config**: When `existingConfig` is not null, each question below MUST present the existing value as the pre-selected default (list it first, marked "(current)"). The user can accept with one click or change it. New fields not in `existingConfig` (e.g., `lspServers` when upgrading from a pre-LSP config) have no default and are asked normally.

| Question | `existingConfig` field | Default when field exists |
|---|---|---|
| 1. Tech stack | `stack` | Pre-fill with formatted stack |
| 2. Project structure | `isMonorepo` | Pre-select based on existing value |
| 3. Branching strategy | `branchPattern` | Pre-fill with existing pattern |
| 5. MCP Servers | `mcpServers` | Pre-select servers where value is `true` |
| 5b. Pencil design | `pencil` | Pre-select based on `pencil.enabled`; if field absent, ask normally |
| 6. LSP Servers | `lspServers` | Pre-select servers where value is `true`; if field absent, ask normally |
| 7. Auto-compact | `autoCompactDisabled` | Pre-select Yes/No |
| 7b. Pin subagents to 200K | `pinSubagents200K` | Pre-select Yes/No |
| 8. CI/CD pipeline | `cicd` | Pre-select Yes/No based on `cicd.enabled` |
| 9. Sandbox Dockerfile | `sandbox` | Pre-select Yes/No based on `sandbox.enabled` |

Ask these questions one at a time using the `AskUserQuestion` tool:

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
- **Lint command**: derive from the Stack-to-CI mapping table's Lint column (see the table under question 8 below); omit `lintCommand` entirely when the detected stack has no Lint row (e.g. `markdown-shell`, `docker-shell`)

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

(There is no question 4 — sandboxing is not asked. The cenci-sandbox container is the security boundary and Claude Code's host sandbox is always disabled; numbering of the remaining questions is kept for stability.)

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
- **plugin**: Already defined in cenci's `.mcp.json`. Enable by setting `disabled: false`.
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
     For maximum token savings, you can disable the Pencil MCP server in your editor settings (Pencil → Preferences → MCP Server). cenci uses the CLI directly and does not need the MCP server."
   - **Fails or not found** → Write `pencil.mode: "editor"` to the config. Inform the user:
     "Pencil `interactive` mode not available. Design skills will use the Pencil MCP server (requires the MCP connection to be active in your editor).
     For better token efficiency, install the `pencil` command from within the Pencil app (File → Install `pencil` command into PATH) and re-run `/cenci:configure` — this switches to `cli-app` mode which avoids loading MCP tool schemas into every conversation."

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

7b. **Pin subagents to 200K context**: "Do you want to pin cenci's subagents to a 200K-context
    model? cenci delegates reviews to subagents, and on 1M-context sessions that delegation can be
    gated — every subagent inherits the session's 1M flag but not its extra-usage entitlement, so
    reviews fail with 'Usage credits required for 1M context' (Claude Code bug #51060). Pinning
    subagents to Sonnet 200K keeps reviews working while your main session keeps its 1M context.
    (Recommended: Yes if you run a 1M-context session; No otherwise — see the tiering caveat below)"
   - Default: Yes when the session plausibly runs a 1M model; No otherwise
   - If Yes: merge `{"env": {"CLAUDE_CODE_SUBAGENT_MODEL": "claude-sonnet-5"}}` into `~/.claude/settings.json` using jq (create the file if it doesn't exist). This runs all `Task` subagents on Sonnet 200K regardless of the main session model. (Pin Sonnet, not Opus — Opus is auto-upgraded to 1M on Max/Team/Enterprise plans and would re-trigger the gate.)
   - If No: remove the `CLAUDE_CODE_SUBAGENT_MODEL` key from the `env` object in `~/.claude/settings.json` (if present)
   - **Tiering caveat (state regardless of answer)**: the pin overrides every agent's `model:` frontmatter — cenci's model tiering (opus planner/security-reviewer, haiku context-gatherer/structure-analyzer/lessons-collector) is flattened onto the pinned model while it is set. On a standard 200K session the 1M gate never fires, so answer No there to keep the tiering active.
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

9. **Sandbox Dockerfile**: "Generate a sandbox Dockerfile for this repo? (Tailors the sandbox image to your stack; committed so your team shares it.)"
   - Options: "Yes — generate `.cenci/Dockerfile`", "No — skip"
   - Default: Yes

   **Agent runtime fragments**: Always include both `node.dockerfile` and `codex.dockerfile`, regardless of the detected project stack. `cenci open --agent codex` executes the Codex CLI baked into the selected image, and the npm-distributed Codex launcher requires Node.js. A generated per-repo image that omits either fragment cannot launch Codex.

   **Stack-to-fragment mapping**: In addition to the mandatory agent runtime fragments, use the detected stack from question 1 (or, for monorepos, the union of every `projects[].stack.framework` value) to select which `sandbox/fragments/*.dockerfile` blocks to include:

   | Detected stack | Fragment |
   |---|---|
   | `dotnet*` | .NET SDK block (version from the token) |
   | `angular*` / `react` / `next` / `vue` / `node` | Node block |
   | `angular*` / `react` / `next` / `vue` | Playwright block (Chromium, for `verify-ui` screenshot capture) |
   | `go` | Go block |
   | `python` | Python + uv block |
   | `rust` | Rust block |

   Playwright is scoped to the frontend-framework tokens, not plain `node` — a Node-based
   backend/API project gets the Node block without paying Chromium's image-size cost for a
   visual-verification step it will never run.

   **Monorepo**: take the union of all `projects[].stack.framework` values, deduplicated — e.g. a repo with a Go API project and a React web client project selects both the Go and Node fragments. Node is still emitted only once because the mandatory runtime set and stack-selected set are deduplicated.

   A stack token that matches no row above (e.g. `markdown-shell`, `docker-shell`) contributes no additional project fragment. This is not an error — the generated Dockerfile still contains the mandatory Node and Codex runtime fragments.

   **.NET version substitution** (the only row with a version-from-token adjustment): `sandbox/fragments/dotnet.dockerfile` ships with `ARG DOTNET_SDK_VERSION=10.0.100` as its own default. When including this fragment, replace that default's version with `<major>.0.100`, where `<major>` is extracted from the stack token using the same extraction as the CI mapping's version-pinning table above (`dotnet10` → `10`) — e.g. a `dotnet8` stack writes `ARG DOTNET_SDK_VERSION=8.0.100`. **Monorepo tie-break**: when multiple projects map to the dotnet fragment with different major versions (e.g. one project on `dotnet8`, another on `dotnet10`), use the **highest** major version found across all matching projects. If no major version can be extracted from the token, leave the fragment's own default (`10.0.100`) unmodified — and add an inline comment immediately after the `ARG DOTNET_SDK_VERSION` line noting the version could not be auto-detected from the stack token and the fragment's default was used instead, e.g. `# .NET version could not be auto-detected from the stack token — using fragment default. See sandbox/README.md to pin manually.` (mirrors the unresolved-`baseVersion` comment pattern in the baseVersion resolution above). The other fragments (node, playwright, go, python, rust, codex) are included verbatim with their own `ARG` defaults unmodified — every fragment `ARG` (including `DOTNET_SDK_VERSION` and `BASE_VERSION`) remains overridable at build time via `--build-arg`, so an unmodified default is never a hard lock-in.

   > **Sync obligation**: `sandbox/fragments/*.dockerfile` is the source of truth for these blocks; the mapping table above mirrors their content and existence, not their byte contents (generation reads the fragment files directly — see step 5e). If a fragment is added, removed, or renamed, this table needs a matching manual update. Low risk in practice — both live in the same monorepo and are maintained together — but currently unenforced by tooling.

   > **Trust / security note**: `.cenci/Dockerfile` is committed to the repo, so it is reviewed like any other file in the PR that adds or changes it. It only runs `docker build` steps assembled from `sandbox/fragments/*.dockerfile` — no arbitrary runtime hooks execute during configure or during the build it produces.

### Auth Verification

Before generating config, verify CLI authentication:

Run `gh auth status` and check it returns authenticated. If not, instruct the user to run `gh auth login` first.

After gathering answers:

1. **Detect existing CLAUDE.md location**:
   - Check if `CLAUDE.md` exists at the repo root: `test -f CLAUDE.md`
   - Check if `.claude/CLAUDE.md` exists: `test -f .claude/CLAUDE.md`
   - **If both exist**: Ask via `AskUserQuestion`: "Found CLAUDE.md at both the repo root and `.claude/CLAUDE.md`. Which location do you want to keep?" Options: "Root (CLAUDE.md)", ".claude/ directory (.claude/CLAUDE.md)". Delete the unchosen file after updating the kept one.
   - **If only root exists**: Ask via `AskUserQuestion`: "Found existing `CLAUDE.md` at the repo root. Do you want to keep it there or move it to `.claude/CLAUDE.md`?" Options: "Keep at root", "Move to .claude/". If moving, delete the root file after creating `.claude/CLAUDE.md`.
   - **If only `.claude/CLAUDE.md` exists**: Keep it in place.
   - **If neither exists**: Create at `.claude/CLAUDE.md` (default).

   **Single-project**: Update the chosen file using the template at `${CLAUDE_PLUGIN_ROOT}/templates/claude-md-root.md` — customize with stack. If the file already has content, merge the template sections into it rather than overwriting (preserve user-added content).

   **Monorepo**: Update the chosen file using the template at `${CLAUDE_PLUGIN_ROOT}/templates/claude-md-root-monorepo.md` instead — customize with project count and populate the Projects table from the discovered projects. If the file already has content, merge the template sections into it rather than overwriting (preserve user-added content).

1b. **Generate `.lsp.json`** (if any LSP servers were selected in question 6):

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

3c. **Ensure board lifecycle labels exist**: `gh issue edit --add-label` fails when the label is missing from the repository, so create the lifecycle set now. Run `gh label list --repo <owner>/<repo> --limit 100 --json name` once, then for each **missing** label run its own `gh label create` call (never modify or recolor a label that already exists):

   ```bash
   gh label create "<name>" --repo <owner>/<repo> --color "<color>" --description "<description>"
   ```

   | Label | Color | Description |
   |---|---|---|
   | `Working` | `FBCA04` | Actively being refined, designed, or implemented |
   | `Refined` | `0E8A16` | Ready for design/implementation |
   | `Design` | `D93F0B` | Design-only ticket — deliverable is a design spec |
   | `Designed` | `5319E7` | Design spec approved |
   | `Planned` | `1D76DB` | Plan on disk, ready to pick up |
   | `In Review` | `A2EEEF` | PR open, under review / CI running |
   | `Implemented` | `6F42C1` | PR merged — done |
   | `Followup` | `C5DEF5` | Deferred/out-of-scope item captured from a session — triage before working |
   | `dispatch-failed` | `b60205` | cenci: dispatched work failed after exhausting its retry budget |
   | `plan-invalid` | `d93f0b` | cenci: ticket is Planned but has no parseable plan file |
   | `reconcile-stuck` | `5319e7` | cenci: reconciliation itself is stuck (apply-retry budget exhausted) |

   This is the canonical color/description table. The lifecycle rows above are self-healed by the skills' own `gh label create … || true` fallbacks; the reconciler-owned `dispatch-failed` / `plan-invalid` / `reconcile-stuck` rows are self-healed instead by cenci's Go `GHMutator.EnsureLabels` (not a skill-level fallback) — see "Reconciler-managed labels" under Board lifecycle labels below.

4. **Create or update `.claude/settings.json`**:

   Write the minimal shape below — Claude Code's host sandbox is disabled because the container is the boundary. Under `--dangerously-skip-permissions` Claude Code ignores `permissions.allow/deny`, but keep the base allow list + deny rules as defense-in-depth for the case where a user runs plain `claude` (no skip-permissions) inside the container, e.g. via `cenci open --shell`.

   ```json
   {
     "sandbox": { "enabled": false },
     "permissions": { "allow": [ … ], "deny": [ … ] }
   }
   ```

   - **Fresh** (no existing settings.json): copy `${CLAUDE_PLUGIN_ROOT}/templates/settings.json` as the base.
   - **Existing** settings.json: read it first, then **replace the entire `sandbox` block** with `{ "enabled": false }` (drop `network`, `excludedCommands`, `autoAllowBashIfSandboxed`, and any other sandbox sub-keys an older config may carry). **Preserve** `permissions.allow` and `permissions.deny` exactly, including user-added entries.
   - Omit `sandbox.network`, `sandbox.excludedCommands`, and `sandbox.autoAllowBashIfSandboxed` entirely — they are meaningless when the sandbox is disabled. Never write `sandbox.network.allowedDomains`.

   Then **append** to it:

   > **IMPORTANT**: All base permissions from the template (`Write`, `Edit`, `Read(~/.claude/plugins/**)`, `Read(//tmp/claude*/**)`, `Edit(//tmp/claude*/**)`, `Bash(cd:*)`, `Bash(git:*)`, `Bash(gh:*)`, `Bash(wc:*)`, `SlashCommand(/goal:*)`, `SlashCommand(/loop:*)`, etc.) **MUST** remain in `permissions.allow`. Only **append** new entries — never remove or replace existing ones. When updating an **existing** `settings.json`, also ensure these base entries are present — add any that are missing (older configs predate them — e.g. `SlashCommand(/goal:*)` for the implement autopilot and `SlashCommand(/loop:*)` for `/cenci:babysit`). The `Read(~/.claude/plugins/**)` rule lets the pipeline read its own plugin files (phase docs resolve to `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/skills/…`) without prompting — it is deliberately scoped to `plugins/` so subagents cannot read session transcripts or global config under `~/.claude/`; the `//tmp/claude*/**` rules cover the `shell-rules` heredoc temp-file pattern and the session scratchpad.

   **Append to `permissions.allow`:**
   - Stack-specific rules (e.g., `Bash(dotnet:*)` for .NET, `Bash(ng:*)` for Angular, `Bash(go:*)` for Go)
   - For each enabled MCP, add its tool permissions. Look up the server's available tools
     and add entries in the format `mcp__<server-name>__<tool-name>` (for project-scoped)
     or `mcp__plugin_cenci_<server-name>__<tool-name>` (for plugin-scoped).
     Known tools:
     - **Context7**: `mcp__plugin_cenci_context7__resolve-library-id`, `mcp__plugin_cenci_context7__query-docs`
     - **Pencil** (only if `pencil.enabled` is `true`): `mcp__pencil__*` (auto-allow all Pencil editor MCP tools — only relevant when Pencil editor is open)
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
   scoped-path `Write(<path>)` allow entries (Claude Code's file permission
   checker never matches scoped-path `Write(<path>)` rules — only scoped-path
   `Edit(<path>)` rules, which also cover Write — so a scoped `Write(<path>)`
   entry is dead weight that produces a startup warning):
   1. For **every** scoped-path `Write(<path>)` entry found in
      `permissions.allow` (not just the known base rule
      `Write(//tmp/claude*/**)`), **replace** it with the corrected
      `Edit(<path>)` form rather than appending the missing entry alongside
      it. Deduplicate as you go: if `permissions.allow` already contains an
      `Edit(<path>)` entry with that same `<path>`, drop the redundant
      `Write(<path>)` entry instead of creating a duplicate `Edit(<path>)`.
   2. If `permissions.allow` contains a blanket `Write(*)` entry, normalize
      it to bare `Write`.
   3. **Verify**: after healing, confirm `permissions.allow` contains no
      remaining scoped-path `Write(<path>)` entry (a blanket bare `Write` or
      `Write(*)`→`Write` normalization result is fine — only scoped-path
      `Write(<path>)` forms are the problem). If any remain, the heal step
      above was incomplete — fix before continuing.

### MCP Server Configuration

For each MCP selected in question 5:

**Plugin-scoped (Context7):**
- In cenci's `.mcp.json` (`${CLAUDE_PLUGIN_ROOT}/.mcp.json`), set `mcpServers.context7.disabled` to `false`
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

5e. **Generate `.cenci/Dockerfile`** (from question 9, only if user selected Yes):

   **Resolve `baseVersion`** — try (a), then fall back to (b), then (c):

   (a) **Dogfooding path**: if the repo being configured contains `sandbox/.claude-plugin/plugin.json` (i.e. this is the `cenci` repo itself), read its `.version` field directly and use that as `baseVersion`.

   (b) **Installed-plugin path**: otherwise, read `~/.claude/plugins/installed_plugins.json` and look for keys matching `sandbox@<marketplace>` under `.plugins` (any marketplace suffix). If more than one key matches, use the entry with the **highest semver** among the matching entries' `.plugins["sandbox@<marketplace>"][0].version` values as `baseVersion` — never an arbitrary/first-found tie-break. When more than one match exists, note in the final completion summary (see the "Report what was created" instruction near the end of this file) which marketplace/version was resolved, so the choice is visible to the user rather than silent.

   **Validation (applies to both (a) and (b))**: before writing a resolved `baseVersion` anywhere (the Dockerfile's `ARG BASE_VERSION=` line or `.claude/config.json`), validate it against a strict version pattern: `^[0-9]+\.[0-9]+\.[0-9]+$` (matching the real plugin.json version format, e.g. `"0.9.0"`). This guards against a compromised marketplace entry or a tampered plugin.json in a fork injecting arbitrary content (embedded newlines, `#` comments, Dockerfile directives, or even a spoofed `# cenci:managed-end` sequence) into a file that's later `docker build`'d. If the resolved value does not match the pattern, treat `baseVersion` as unresolved and fall through to (c) — do not write the raw value into the Dockerfile or config.json.

   (c) **Unresolved**: if neither (a) nor (b) yields a version that passes validation, `baseVersion` is unresolved. Store `"baseVersion": null` in `.claude/config.json`'s `sandbox` field. The Dockerfile is still generated (fragments are still selected and written) — the `ARG BASE_VERSION=` line falls back to `latest` (matching `sandbox/Dockerfile`'s own default and the `cenci-sandbox-base:latest` alias tag that `cenci sandbox build-base` always produces), followed by an inline comment pointing at `sandbox/README.md` for a manual pin. An empty default must never be written: Docker's `InvalidDefaultArgInFrom` lint check flags any `ARG` used in a `FROM` whose default resolves to an empty or invalid image reference, and it evaluates the file statically — it fires on every build regardless of the `--build-arg BASE_VERSION=...` override `cenci sandbox build`'s per-repo image build always passes at build time.

   **Generated file format** — always emit the ARG/FROM pair below, **never** a literal `FROM cenci-sandbox-base:<version>`. `cenci sandbox build` always passes `--build-arg BASE_VERSION=...` at build time; a literal `FROM` would silently drift from what's actually built:

   ```dockerfile
   # cenci:managed-begin
   ARG BASE_VERSION=<resolved-version-or-latest>
   FROM cenci-sandbox-base:${BASE_VERSION}

   SHELL ["/bin/bash", "-o", "pipefail", "-c"]

   <selected fragment 1 content, from sandbox/fragments/*.dockerfile>

   <selected fragment 2 content>
   ...
   # cenci:managed-end
   ```

   - If `baseVersion` resolved (path a or b): write it as the ARG default, e.g. `ARG BASE_VERSION=0.9.0`.
   - If unresolved (path c): write `ARG BASE_VERSION=latest`, then a comment line immediately after: `# No cenci-sandbox plugin version detected — using the :latest base image alias. See sandbox/README.md to pin BASE_VERSION manually, or install the cenci-sandbox plugin and re-run /cenci:configure.`

   **Fragment concatenation order** (when multiple fragments apply, e.g. a monorepo union): **dotnet → node → playwright → go → python → rust → codex**, regardless of the order projects were discovered in. Node and Codex are mandatory; the remaining fragments are stack-selected. Concatenate the selected `sandbox/fragments/*.dockerfile` file contents in that fixed order, applying the **.NET version substitution** from the mapping table above to the dotnet fragment only — every other fragment is included verbatim. Deduplicate — each fragment appears at most once even when multiple monorepo projects map to the same fragment.

   **Merge-safe regeneration**: the whole block above (from `# cenci:managed-begin` through `# cenci:managed-end` inclusive) is the managed block.
   - **File doesn't exist**: create `.cenci/` (`mkdir -p .cenci`) and write the managed block as the full file content.
   - **File exists with both markers present**: replace only the text from `# cenci:managed-begin` through `# cenci:managed-end` (inclusive) with the freshly generated block. Preserve everything before the begin marker and everything after the end marker exactly as-is — this is where a team can hand-append their own `RUN` steps across re-runs.
   - **File exists with no markers** (e.g. a hand-authored legacy `.cenci/Dockerfile`): do not silently overwrite it. Reuse the exact Overwrite/Skip/Show conflict-check UX already used for CI/CD generation (question 8 / step 5d) — same three options, same behavior:
     "Found existing `.cenci/Dockerfile` without cenci's managed markers. What would you like to do?"
     Options: "Overwrite — wrap it in managed markers and replace with the generated block", "Skip — keep the existing file, still record `sandbox` in config.json", "Show existing — display the current file contents"
     - If Skip: still record `sandbox` in config.json, don't write the file.
     - If Show existing: read and display the file, then re-ask Overwrite/Skip.
   - **File exists with malformed markers** (exactly one of `# cenci:managed-begin` / `# cenci:managed-end` present, markers out of order, or duplicate marker pairs): do **not** attempt a partial text replace — a malformed marker pair cannot be trusted to safely bound the managed block (and could itself be the result of a spoofed end-marker smuggled in via an unvalidated `baseVersion` — see the validation step above). Route this through the exact same Overwrite/Skip/Show conflict-check UX as the "no markers" case above — same prompt text, same three options, same behavior.

   **Monorepo**: fragments are the mandatory Node and Codex runtime fragments plus the deduplicated union described in the Stack-to-fragment mapping table under question 9, concatenated in the dotnet → node → playwright → go → python → rust → codex order above — one `.cenci/Dockerfile` for the whole repo, not one per project.

   **Committed, not ignored**: `.cenci/Dockerfile` is committed to the repo. Do **not** add `.cenci/` or `.cenci/Dockerfile` to `.gitignore` — the whole point is a team-shared, reviewed Dockerfile that the launcher's per-repo image selection (see `sandbox/README.md`) builds identically for every teammate.

6. **Write `.claude/config.json`** with their choices using **merge semantics**:

   - If `existingConfig` is not null: start from the existing object, overwrite each field with the user's answers. This preserves fields the skill doesn't manage.
   - If `existingConfig` is null: create the file fresh.

```json
{
  "branchPattern": "feature/<id>-<description>",
  "stack": {
    "backend": "dotnet10",
    "frontend": "angular21",
    "testing": ["xunit", "jasmine"]
  },
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
  "cenci": {
    "compactImplementation": false,
    "reviewConcurrency": "parallel",
    "diffContextMode": "inline",
    "liteReviewEnabled": true,
    "goalAutopilot": true,
    "planComment": false
  },
  "cicd": {
    "enabled": true,
    "platform": "github-actions"
  },
  "sandbox": {
    "enabled": true,
    "baseVersion": "0.9.0"
  }
}
```

The `cenci` field is optional. If present, preserve existing user values during reconfiguration. Schema:
- `compactImplementation` — `true` allows small, low-risk tickets to combine red/green/refactor into one implementer subagent turn while preserving all TDD/reporting gates. Default: `false`.
- `reviewConcurrency` — `"parallel"` runs security, code, and silent-failure reviews together; `"sequential"` runs the same reviews one after another to smooth usage limits. Default: `"parallel"`.
- `diffContextMode` — `"inline"` passes small diffs directly to reviewers; `"file"` writes the diff to `/tmp/claude/cenci-<ticket-id-or-slug>-diff.patch` and passes paths so reviewers read targeted hunks. Default: `"inline"`.
- `liteReviewEnabled` — `true` (default) lets Phase 6 + 7 classify each diff into `full` (all three reviewers), `lite-docs` (no reviewers, docs-only), or `lite-small` (`code-reviewer` only, small config/data-only diffs); `false` forces the full trio on every run regardless of diff size or content. See Phase 6 + 7 for the precedence-ordered classification rules.
- `goalAutopilot` — `true` arms a native `/goal` completion condition at Phase 2 start (Claude Code ≥ 2.1.139) so implement phases 2–9 resume through to an open PR after a mid-phase stop; `false` opts out. Default (unset): enabled where supported, a graceful no-op on older runtimes.
- `planComment` — `true` makes implement Phase 1 also post the saved plan as a ticket comment (ticket mode only) right after marking the ticket `Planned`, for audit / off-host visibility; `.plans/` stays the executable source of truth. Default: `false` (no comment).

Configure always writes `sandbox: { "enabled": false }` in `.claude/settings.json` (no `network`/`excludedCommands`/`autoAllowBashIfSandboxed`) — the cenci-sandbox container is the security boundary. The config no longer carries `profile` or `sandboxEnabled` fields; re-config strips them from older configs (see the migration-removal list below).

Optional external usage reducer: RTK (`https://github.com/rtk-ai/rtk`) can compress shell command output before it reaches Claude Code. It is not required for cenci and should not be installed automatically, but it is worth recommending when users are hitting usage limits from command-heavy sessions. After separate installation, `rtk init -g` enables Claude Code Bash command rewriting where supported. Built-in tools like `Read`, `Grep`, and `Glob` do not pass through RTK hooks.

The `cicd` field is only present when the user selected Yes in question 8. Schema:
- `cicd.enabled` — `true` if user opted in, omit `cicd` entirely if declined
- `cicd.platform` — `"github-actions"`

Omit `cicd` entirely when the user says No (same pattern as `pencil`).

The `sandbox` field is only present when the user selected Yes in question 9. Schema:
- `sandbox.enabled` — `true` if user opted in; omit `sandbox` entirely if declined (same pattern as `cicd`/`pencil` — never write `{"enabled": false}`)
- `sandbox.baseVersion` — the resolved sandbox plugin version baked into the generated `.cenci/Dockerfile`'s `ARG BASE_VERSION` default (see the baseVersion resolution algorithm in step 5e), or `null` when it could not be resolved

Omit `sandbox` entirely when the user says No (same pattern as `cicd`/`pencil`).

> **Not the same as `.claude/settings.json`'s `sandbox.enabled`.** Step 4 above always writes `"sandbox": { "enabled": false }` into `.claude/settings.json` — that key disables **Claude Code's own host sandbox**, because the cenci-sandbox container is the security boundary instead. This `.claude/config.json` `sandbox` field is unrelated: it's this ticket's per-repo `.cenci/Dockerfile` toggle, consumed by cenci's configure skill and by `cenci sandbox build`'s per-repo image build — not by Claude Code itself. Same field name (`sandbox.enabled`), two different files, two different consumers, two unrelated meanings. Do not conflate them when reading or writing either file.

The `pencil` field is only present when the user was asked question 5b (frontend framework detected). Schema:
- `pencil.enabled` — gating flag for all design features (`true` if user opted in, `false` if declined)
- `pencil.designPath` — where `.pen` and `DESIGN.md` files live (default: `"designs/"`)
- `pencil.mode` — Pencil connection mode: `"editor"` (default, GUI with MCP), `"headless"` (future, npm package), or `"auto"` (future, try headless then editor)
- `pencil.shared` — only present when `isMonorepo: true` and user chose shared design files (`true` for shared, omitted for separate)

Omit `pencil` entirely if no frontend framework was detected.

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

**Monorepo config** — when `isMonorepo` is true, add `isMonorepo` and `projects` fields:

```json
{
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
      "lintCommand": "dotnet format --verify-no-changes"
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
  },
  "sandbox": {
    "enabled": true,
    "baseVersion": "0.9.0"
  }
}
```

Each project entry's `lintCommand` is optional — it is omitted for stacks with no Lint row in the Stack-to-CI mapping table (e.g. `markdown-shell`, `docker-shell`), the same way `buildCommand`/`testCommand` are already handled for stacks without one.

Existing single-project configs (no `isMonorepo` field) work unchanged.

Only include servers in `mcpServers` that were presented as options (i.e., detected or always-available). Value is `true` if enabled, `false` if declined.

Only include servers in `lspServers` that were detected and presented in question 6. Value is `true` if enabled, `false` if declined. Omit `lspServers` entirely if no LSP servers were detected.

When migrating from an older config that has `ticketSystem`, `prSystem`, `ticketPrefix`, `adoOrg`, `adoProject`, `adoRepo`, `profile`, or `sandboxEnabled` fields, remove them during the merge.

**Monorepo `pencil` notes:**
- When `pencil.shared` is `true`: `pencil.designPath` holds the shared path (e.g., `"designs/"`). Individual projects do **not** have `designPath`.
- When `pencil.shared` is `false` (separate): `pencil.designPath` is omitted. Each frontend project in the `projects` array gets a `designPath` field (e.g., `"apps/web-client/designs/"`). Non-frontend projects do not get `designPath`.

Report what was created and suggest next steps (e.g., "Try `/cenci:refine <ticket-id>` on a ticket"). If `sandbox.enabled` is `true`, mention the generated/committed `.cenci/Dockerfile` and that `cenci sandbox build` (run from inside the repo) builds the repo's own tailored image on top of the shared base. If `sandbox.baseVersion` resolved to `null` (unresolved — see the baseVersion resolution in step 5e), the chat summary must explicitly say so (e.g., "Base version could not be auto-detected — see `sandbox/README.md` to pin `BASE_VERSION` manually") rather than leaving it only as an inline Dockerfile comment, so a user who doesn't open the generated file still learns the base version wasn't pinned.

### Board lifecycle labels

configure creates any missing lifecycle labels during project setup (step 3c) — `gh issue edit
--add-label` fails when a label is missing from the repository, which used to silently drop
labels (e.g. `Planned`) on projects configured before a label was introduced. The skills apply
the labels via `gh issue edit` as a ticket moves through the workflow, and self-heal with a
`gh label create … || true` before adding a label that may be missing. Document the label set
in the completion summary so the user can mirror it as columns on their board:

| Label | Applied by | Meaning |
|---|---|---|
| `Working` | refine / design / implement (at start) | Actively being refined, designed, or implemented |
| `Refined` | refine | Ready for design/implementation |
| `Design` | refine | Design-only ticket — deliverable is a design spec; implement redirects to `/cenci:design` |
| `Designed` | design | Design spec approved — propagated from the completed design ticket to the implementation tickets that depend on it |
| `Planned` | implement Phase 1 (plan persisted) | Plan on disk, ready to pick up |
| `In Review` | implement Phase 9 (at PR-open) | PR is open, under review / CI running |
| `Implemented` | babysit (on PR merge) | PR merged — done |
| `Followup` | implement Phase 9 / address-review | Deferred/out-of-scope item captured from a session — triage before working; enters backlog unrefined |

`Followup` is orthogonal to the linear lifecycle above — it is never part of the `New → … → Implemented` chain, applies to a separate followup ticket (not the original), and is never removed.

A ticket judged trivial by implement's Trivial-Ticket Triage still transits through `Planned` — same labels, same board columns as any other ticket — just collapsed into one session instead of a stop-and-relaunch between `Planned` and `Working`. No new label state is introduced by the trivial-ticket fast path.

**Reconciler-managed labels**: `dispatch-failed`, `plan-invalid`, and `reconcile-stuck` are not applied by refine, design, or implement — cenci's dispatch reconciler applies them automatically when it detects a stuck or failed automation state (dispatched work exhausted its retry budget, a `Planned` ticket has no parseable plan file, or reconciliation itself is stuck with its apply-retry budget exhausted). Like `Followup`, they are orthogonal to the `New → … → Implemented` lifecycle above and are not columns in that chain.

Lifecycle: `New → Refined → [Designed] → Planned → Working → In Review → Implemented`.

Design always happens on a dedicated design ticket — refine creates one (companion ticket, or first child of a split) whenever a frontend ticket lacks an approved design. Design tickets (labeled `Design`) follow a shorter path: `New → Refined → Designed → closed` — `/cenci:design` commits the spec on main, propagates `Designed` to the implementation tickets that depend on it (that propagated label is what satisfies implement's Design gate), and closes the design ticket; no plan, no PR (the one exception to "1 ticket = 1 PR"). `/cenci:implement` redirects `Design`-labeled tickets to `/cenci:design`. On a board, the `Designed` column therefore holds implementation tickets whose design is ready, not design tickets.

**Migration note for existing boards** (state this when re-configuring a project that predates
`In Review`): add an **`In Review`** column/label. Previously, tickets dropped straight into
`Implemented` the moment the PR was opened; now PR-open lands them in `In Review`, and
`/cenci:babysit` promotes them to `Implemented` when the PR actually merges. Existing tickets
that carry `Implemented` from before this change but whose PRs are still open can be relabeled to
`In Review` by hand; new work follows the split automatically.

**Migration note for existing boards** (state this when re-configuring a project that predates
`Planned`): add a **`Planned`** column/label between `Designed` and `Working`. Previously, a
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
