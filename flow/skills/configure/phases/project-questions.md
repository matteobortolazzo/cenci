# Project Questions

Read this file on every run, after `phases/detection-and-delta.md` (see `SKILL.md` `### Step Map`). It holds questions 1 to 3 — tech stack, project structure with Steps 2a and 2b, and branching strategy. Ask each only when its ID is in `delta.ask`, as `SKILL.md` `### Question Defaults` describes.

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
- **Lint command**: derive from the Stack-to-CI mapping table's Lint column (see the table under question 8 in `phases/runtime-ci-sandbox-questions.md`); omit `lintCommand` entirely when the detected stack has no Lint row (e.g. `markdown-shell`, `docker-shell`)

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
