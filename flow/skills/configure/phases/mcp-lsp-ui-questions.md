# MCP, LSP and UI Questions

Read this file on every run, after `phases/project-questions.md` (see `SKILL.md` `### Step Map`). It holds the dependency detection and its manual fallback, the MCP and LSP server catalogs, questions 5 and 6, UI conventions (5c), Playwright CLI setup, LSP detection and binary verification. Ask each question only when its ID is in `delta.ask`, as `SKILL.md` `### Question Defaults` describes; the catalogs and fallbacks are read on every run because generation step 1b and the MCP configuration in `phases/guidance-settings.md` use them.

### Dependency Detection

The dependency scan is scripted: `detection.mcpServers` carries the MCP catalog triggers found, and `detection.dindDetected` is the `dindDetected` value question 9b uses in `phases/runtime-ci-sandbox-questions.md`.

**Manual fallback** (only if Scripted Detection failed) — scan the project for framework dependencies:

1. If `package.json` exists in the repo root, read `dependencies` and `devDependencies`
2. If `.csproj` files exist, read `PackageReference` entries
3. Store the detected package names for matching against the MCP catalog below
4. **Testcontainers/Docker-SDK detection** (for question 9b in `phases/runtime-ci-sandbox-questions.md` — nested Docker/dind): scan for these triggers and store the result as `dindDetected` (`true` if any match, `false` otherwise):
   - npm: `testcontainers`, `@testcontainers/*`, or `dockerode` in `dependencies`/`devDependencies`
   - NuGet: `Testcontainers*` or `Docker.DotNet` in `.csproj` `PackageReference` entries
   - Python: `testcontainers` in the dependency list (`requirements.txt`, `pyproject.toml`) — dep list only, no source scan
   - Go: `github.com/testcontainers/testcontainers-go` in `go.mod`

### MCP Server Catalog

| Trigger Package | Server Name | Command | Args | Env Vars | Scope |
|---|---|---|---|---|---|
| *(always available)* | context7 | `npx` | `["-y", "@upstash/context7-mcp@3.2.5"]` | `CONTEXT7_API_KEY` | project |
| `@angular/core` | angular | `npx` | `["-y", "@angular/cli", "mcp"]` | — | project |
| `primeng` | primeng | `npx` | `["-y", "@primeng/mcp"]` | — | project |

**Scope:**
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

   **Keep-or-change gate**: if `existingConfig.mcpServers` is present, ask the Keep/Change gate in `SKILL.md` `### Question Defaults` before the multi-select — **Yes** carries `existingConfig.mcpServers` forward unchanged and skips the multi-select below entirely, **No** continues to the multi-select below. If `existingConfig.mcpServers` is absent (first-ever configure run, or a newly-detected MCP not previously offered), skip the gate and ask the multi-select directly.

   Present using AskUserQuestion with multiSelect=true (sort currently-enabled servers first when `existingConfig.mcpServers` exists, so they're easiest to re-tick):

   "Based on your project dependencies, these MCP servers can enhance AI assistance.
    Which would you like to enable?"

   Options (only show those whose trigger was detected, plus Context7 always):
   - "Context7 — Live documentation lookup for any library (requires free API key from context7.com/dashboard)"
   - "Angular — Official Angular AI tutor, best practices, and documentation search"
   - "PrimeNG — Component documentation, props, events, theming, and examples"

   If only Context7 is available (no framework-specific MCPs detected), still present it:
   "Do you want to enable Context7 for live documentation lookup?
    (Requires a free API key from context7.com/dashboard)"

### UI Conventions

**Condition**: Only ask question 5c when a frontend framework is detected in the stack from
question 1. Frontend frameworks include: Angular, React, Next.js, Vue, Svelte, or any UI
framework.

If no frontend framework is detected, skip this section entirely.

5c. **UI conventions**: The answers here become guidance prose in the generated `AGENTS.md`
   `## UI Conventions` section — they are **not** written to `.cenci/config.json`. There is no
   config key for this.

   Ask using AskUserQuestion:

   "Your project includes `<detected-frontend-framework>`. Where does its shared component
    library live? (Path relative to the repo root — leave blank if there isn't one yet.)"

   Options: offer any directory the repository scan suggests — for example a `components/`,
   `ui/`, or `design-system/` directory, or a workspace library under `libs/` or `packages/` —
   plus "None yet".

   Then ask:

   "Is there a component catalog app — Storybook, an Angular component workshop, or a similar
    browsable gallery — and how is it run?"

   Options: offer the detected candidates — a `storybook` or `ng serve <app>` script in
   `package.json`, an `.storybook/` directory, or a dedicated showcase project in the
   `projects` array — plus "None".

   **Writing the answers**: fill the `## UI Conventions` block in the generated `AGENTS.md`
   (from `templates/agents-md-project.md` for a monorepo project, or
   `templates/agents-md-root.md` / `templates/agents-md-root-monorepo.md` for the root):
   - `<path-to-component-library>` becomes the library path; drop the bullet when "None yet".
   - `<storybook-like-app-path>` and `<catalog-command>` become the catalog's path and its run
     command; drop the bullet when "None".
   - `<project-specific UI rules populated during configure>` stays as a placeholder for the
     user to fill in later, or is removed when no conventions are known yet.

   Keep the reuse-before-authoring bullet verbatim in every case — it is the rule the planner
   and implementer look for, and it holds whether or not a library exists yet.

   In a monorepo, ask per frontend project when the projects have different libraries; ask
   once and write the shared answer into the root `AGENTS.md` when they share one.

### Playwright CLI Setup

**Condition**: Only ask this when a frontend framework is detected in the stack from question 1 AND `detection.playwrightTest` is `true` (manual fallback: `@playwright/test` found in root `devDependencies`).

If both conditions are met, present using AskUserQuestion:

   "Your project uses Playwright Test. Do you want to set up Playwright CLI (`@playwright/cli`) for interactive browser automation during development?
    (Screenshots, snapshots, form filling, network inspection — more token-efficient than Chrome MCP for agents.)"

   Options: "Yes — install and configure Playwright CLI", "No — skip"

   **If Yes**:
   1. Check if `playwright-cli` is already installed: `which playwright-cli 2>/dev/null`
      - **Found** → "✓ `playwright-cli` found at `<path>`"
      - **Not found** → "Run `npm i -g @playwright/cli` to install, then `playwright-cli install --skills` to set up agent skills."
   2. Set `playwrightCli: true` in `.cenci/config.json`

   **If No**: Set `playwrightCli: false` in `.cenci/config.json` (or omit the field)

If the conditions are not met, skip this section entirely (do not set `playwrightCli` in config).

### LSP Detection

`detection.lspServers` carries the detected LSP catalog triggers directly.

**Manual fallback** (only if Scripted Detection failed) — reuse the dependency detection results from earlier and add file-type detection to match against the LSP Server Catalog:

- `typescript`, `@angular/core`, `react`, `next`, or `vue` in `package.json` dependencies → **typescript**
- `*.py` files present, or `pyproject.toml`, or `requirements.txt` → **pyright**
- `Cargo.toml` present → **rust-analyzer**
- `*.csproj` present → **csharp-ls**
- `go.mod` present → **gopls**

If no LSP servers are detected, skip question 6 entirely.

6. **LSP Servers**: If **two or more** LSP servers were detected above:

   **Keep-or-change gate**: if `existingConfig.lspServers` is present, ask the Keep/Change gate in `SKILL.md` `### Question Defaults` before the multi-select ("Current LSP servers: …") — **Yes** carries `existingConfig.lspServers` forward unchanged and skips the multi-select below entirely, **No** continues to the multi-select below. If `existingConfig.lspServers` is absent (first-ever configure run, or a newly-detected LSP server not previously offered), skip the gate and ask the multi-select directly.

   Present using AskUserQuestion with multiSelect=true (sort currently-enabled servers first when `existingConfig.lspServers` exists, so they're easiest to re-tick):

   "LSP servers provide real-time diagnostics (type errors, unused variables, dead code) during implementation. Based on your project, which would you like to enable?"

   Options (only show those whose trigger was detected):
   - "typescript — TypeScript/JavaScript type checking and diagnostics"
   - "pyright — Python type checking and diagnostics"
   - "rust-analyzer — Rust type checking and diagnostics"
   - "csharp-ls — C# type checking and diagnostics"
   - "gopls — Go type checking and diagnostics"

   If exactly **one** LSP server was detected, ask a plain Yes/No instead — a multi-select checkbox with a single option has no sensible "none" choice:

   "Your project uses `<detected-language>`. Do you want to enable `<server-name>` for real-time diagnostics (type errors, unused variables, dead code) during implementation?"

   Options: "Yes — enable `<server-name>`", "No — skip"

#### Binary Verification

For each LSP server the user selected, verify the binary is installed:

```bash
which <command>
```

- **Found**: Confirm with a checkmark: "✓ `<command>` found at `<path>`"
- **Not found**: Warn with install command: "⚠ `<command>` not found. Install with: `<install-command>`. Server will activate once installed."

Include the server in `.lsp.json` regardless — it activates once the binary is installed.
