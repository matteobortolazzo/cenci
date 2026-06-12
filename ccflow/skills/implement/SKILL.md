---
name: implement
description: Full implementation pipeline — plan, test, implement, review, PR
argument-hint: <ticket-id | task description> [additional context]
user-invocable: true
disable-model-invocation: true
model: sonnet
allowed-tools: Read, Write, Edit, Bash, Glob, Grep, Task, AskUserQuestion, mcp__context7, mcp__pencil__batch_get, mcp__pencil__get_variables, mcp__pencil__get_screenshot, mcp__pencil__snapshot_layout, mcp__pencil__get_editor_state
---

Read the `subagent-safety` reference skill before delegating work to subagents.

## Context

**Config check**: Before anything else, verify `.claude/config.json` exists by reading it. If the file does not exist, **stop immediately** and tell the user:
"ccflow is not configured for this project. Run `/ccflow:configure` first to set up."

Read `.claude/config.json`.
Read the `claudeMdLocation` field from `.claude/config.json` to determine where `CLAUDE.md` is located (defaults to `.claude/CLAUDE.md` if not set).

> **Progressive disclosure**: Do NOT eagerly read reference docs in this Context section. The planner subagent reads relevant `docs/<topic>.md` files (and any legacy `.claude/rules/lessons-learned.md` if present) as part of its analysis. `docs/git-workflow.md` is only consulted in Phase 9 (commits/PRs). `.claude/rules/` is reserved for files explicitly `@`-imported by `CLAUDE.md`; do not assume anything lives there.

### Monorepo Context Loading

If `isMonorepo` is `true` in `.claude/config.json`:

1. **Determine affected project(s)**: From the ticket description and file paths, match against the `projects` array in config to identify which project(s) the ticket affects.
2. **Read per-project CLAUDE.md**: For each affected project, read `<project-path>/CLAUDE.md` for project-specific stack details and conventions.
3. **Use project-specific commands**: When delegating to subagents, use the project's `buildCommand` and `testCommand` from config instead of inferring them globally.
4. **Pass project context to subagents**: When delegating to planner/implementer, include the per-project CLAUDE.md content. Tell the subagent to read relevant `docs/<topic>.md` files (and the legacy `.claude/rules/lessons-learned.md` or `.claude/rules/lessons-learned-<slug>.md` if those legacy files exist). Do not pre-read those in the main agent.

### Design Context Loading

If `pencil.enabled` is `true` in `.claude/config.json`:

1. **Determine design path**: Read `pencil.designPath` from config. If the project is a monorepo with `pencil.shared: false`, use the per-project `designPath` from the affected project's entry in the `projects` array.
2. **Load DESIGN.md**: If `<designPath>/DESIGN.md` exists, read it and store as `designSpec`. This contains screen-to-route mappings, component-to-code mappings, design tokens, and naming conventions.
3. **Note .pen file path**: Record the `.pen` file path from the DESIGN.md header for planner reference. Do not read the `.pen` file yet — subagents cannot use Pencil tools, so `.pen` content must be pre-read by the main agent if needed.
4. **Parse design structure from DESIGN.md** (if loaded):
   - Extract screen node IDs from the Screens table (these are Pencil node identifiers)
   - Extract component node IDs and their framework component mappings from the Components table
   - Extract design token references (CSS custom properties) from the Design Tokens section
   - Store these parsed values as `designScreenIds`, `designComponentMap`, and `designTokens` for use in Phase 1 (planner) and Phase 4 (implementer)
5. **Pencil availability probe**: Read `pencil.mode` from config (default: `"editor"`). Store as `$PENCIL_MODE`. Before any Pencil calls later in the pipeline, attempt a lightweight probe:

   **CLI-app mode** (`pencil.mode` is `"cli-app"`):
   ```bash
   pencil interactive -a desktop <<'EOF'
   get_editor_state({ include_schema: false })
   EOF
   ```
   If it succeeds → set `pencilAvailable = true`. If it fails → set `pencilAvailable = false`.

   **Editor mode** (`pencil.mode` is `"editor"`):
   ```
   Call `get_editor_state()` via MCP — if it succeeds, set `pencilAvailable = true`.
   If it fails or times out, set `pencilAvailable = false`.
   ```

   If the probe fails, inform the user: "Pencil unavailable — proceeding with DESIGN.md text content only. Open Pencil and retry if live design reads are needed."
   This probe runs once during context loading. Do not auto-launch Pencil.

If `pencil.enabled` is not `true` or `pencil` is absent, skip this section.

**Shell rules**: Read the `shell-rules` skill before running any `gh` commands (covers heredoc temp-file pattern).

**Parse `$ARGUMENTS` — Mode Detection:**

Extract the first whitespace-delimited token from `$ARGUMENTS` and determine the mode:

- **If the first token matches `^\d+$` or `^#\d+$`** → **ticket mode**
  - Strip any `#` prefix to get the numeric ticket ID.
  - Everything after the first token is optional **user context** (additional instructions or focus areas).
  - Examples: `#1 focus on API` → ID `1`, context `focus on API`; `7` → ID `7`, no context.

- **If the first token ends in `.md` and resolves to a file in `.plans/`** → **plan file mode**
  - Read the plan file. Parse the YAML front matter (between `---` delimiters) to extract metadata: `version`, `mode`, `ticketId`, `ticketTitle`, `slug`, `isChild`, `isLastChild`, `parentId`, `planCommitSha`, `createdAt`, `status`.
  - Set `hasPlanFile = true`.
  - Inherit the original mode (`ticket` or `ticketless`) from the front matter's `mode` field.
  - If `mode` is `ticket`, set the ticket ID and slug from front matter. If `mode` is `ticketless`, set the slug from front matter.
  - The rest of `$ARGUMENTS` after the file path is ignored.

- **Otherwise** → **ticketless mode**
  - The entire `$ARGUMENTS` string is the **task description**.
  - Generate a **slug** from the description: take the first 4–5 meaningful words, lowercase, hyphenated.
    For example: `add dark mode support for the dashboard` → slug `add-dark-mode-support`.
  - There is no ticket ID and no separate user context — the task description is the primary input.

The determined mode (ticket or ticketless) governs conditional behavior throughout the rest of this skill.

**Plan file auto-detection** (ticket mode only): If the first token is a ticket ID (ticket mode) and a file matching `.plans/<id>-*.md` exists, present the user with a choice using `AskUserQuestion`:
- **"Use existing plan"** — switch to plan file mode, set `hasPlanFile = true`, read the plan file
- **"Re-plan from scratch"** — ignore the plan file, proceed with normal ticket mode

**If ticket mode:** Fetch the ticket:
Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`), then run:
```bash
gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,comments
```

**If ticketless mode:** Skip ticket fetching. The task description from `$ARGUMENTS` is the primary input.

## Parent-Child Detection

**If ticketless mode:** Skip this section entirely.

**If ticket mode:** After fetching the ticket, detect whether this is a child ticket created by `/ccflow:refine` splitting:

1. **Identify parent**: Parse the ticket body for `Related to #<number>`. If found, this is a child ticket — extract the parent ID.

2. **Fetch the parent ticket**: Use the same fetch command as above (`gh issue view`). Check the parent's `state` — if already closed, set `isChild = true`, `isLastChild = false`, `parentId = <id>` and skip to Attachments (don't try to close an already-closed parent).

3. **Find siblings**: Look for a `### Child Tickets` section in the parent's body. Extract sibling issue numbers from lines matching `- [ ] #<number>` or `- [x] #<number>`.

   **Fallback** if no `### Child Tickets` section exists: search for siblings via:
   ```bash
   gh issue list --repo <owner>/<repo> --search "\"Related to #<parentId>\"" --state all --json number
   ```

4. **Determine if last child**: Check how many siblings are still open (excluding the current ticket):
   ```bash
   gh issue list --repo <owner>/<repo> --search "\"Related to #<parentId>\"" --state open --json number
   ```
   If the only open sibling is the current ticket → `isLastChild = true`

5. **Store state** for later use in commit, PR body, and labeling:
   - `isChild` — whether this ticket has a parent
   - `isLastChild` — whether this is the last open child (triggers parent auto-close)
   - `parentId` — the parent ticket number/ID

**Edge cases:**
- Parent already closed → `isLastChild = false` (skip auto-close)
- No `### Child Tickets` section on parent → use search fallback
- Some siblings manually closed → they don't count as open, don't block last-child detection

## Attachments

**If ticketless mode:** Skip the Attachments section entirely and proceed to Pre-flight Check.

**If ticket mode:** Read the `attachments` reference skill and follow its 4-step procedure to discover, present, download, and load ticket attachments. If no attachments are found or the user selects none, proceed to Pre-flight Check.

Store each attachment's file path for passing to subagents (subagents share the filesystem and can read attachments directly via `Read`).

## Pre-flight Check

### Settings Verification

**Before fetching the ticket (or before proceeding in ticketless mode)**, read `.claude/settings.json` and `.claude/config.json` and verify the required permissions are present:

1. Check `permissions.allow` in `.claude/settings.json` contains **at minimum**:
   - `Write(*)`
   - `Edit(*)`
2. Read `.claude/config.json` and check feature-specific permissions in `permissions.allow`:
   - If `mcpServers` exists in config, for each server where value is `true`:
     verify its tool permissions exist in `permissions.allow`
     (Context7: `mcp__plugin_ccflow_context7__resolve-library-id` and `mcp__plugin_ccflow_context7__query-docs`;
      project MCPs: `mcp__<name>__*`)
   - Legacy support: if `context7Enabled: true` exists (no `mcpServers` field), treat as `mcpServers.context7: true`
   - Verify `Bash(gh *)` exists
3. Verify CLI authentication:
   - Run `gh auth status` and verify it returns authenticated

If any permissions are missing, **offer to auto-fix** by appending the missing entries:

> "Missing permissions in `.claude/settings.json`: [list missing items]. This will cause permission dialogs during the pipeline.
> I can auto-fix this by appending the missing entries to `.claude/settings.json`. Want me to fix it?"

If the user approves the auto-fix:
1. Read `.claude/settings.json`
2. Determine the **full set** of missing permissions to append
3. Filter out any entries already present in `permissions.allow`
4. Append only the missing entries to the `permissions.allow` array
5. Write the updated `.claude/settings.json` back
6. Confirm: "Fixed! Added [N] missing permissions. Continuing..."

If the user declines the auto-fix:
> "OK, proceeding without fixing. You may see permission dialogs during the pipeline. Want to continue anyway?"

If the user says no → stop. If yes → proceed.

### Ticket Readiness

**If ticketless mode:** Skip the Ticket Readiness check entirely and proceed to the Pipeline.

**If ticket mode:** After fetching the ticket, inspect its labels/tags before starting the pipeline:

Check the issue's `labels` array.

If the ticket does **not** have a "Refined" label/tag, display a warning:
> "This ticket hasn't been refined yet. Consider running `/ccflow:refine <ticket-id>` first for better results. Do you want to proceed anyway?"

If the user says no → stop. If yes → proceed with the pipeline.

#### Design Check (soft)

If the ticket is classified as frontend — its title, description, or acceptance criteria mention UI components, pages, views, layouts, forms, modals, visual design, styling, CSS, animations, themes, or frontend frameworks (React, Angular, Vue, Svelte, etc.) — and does **not** have a "Designed" label/tag **and** `designSpec` was not loaded (no DESIGN.md found), display a suggestion:
> "This frontend ticket hasn't been designed yet. Consider running `/ccflow:design <ticket-id>` first for a visual reference. Do you want to proceed anyway?"

If the ticket lacks the "Designed" label but a `DESIGN.md` exists (loaded as `designSpec`), skip the suggestion — the design spec is sufficient context.

If the user says no → stop. If yes → proceed with the pipeline. This is a soft-check — it never blocks implementation.

#### Visual Check Reminder

If the ticket has a `ui:visual-check` or `Browser` label, display a reminder:
> "This ticket has the `ui:visual-check` label. Ensure `playwright-cli` is available for visual verification (`playwright-cli screenshot`, `playwright-cli snapshot`)."

This is informational only — it does not block the pipeline.

## Label "Working"

**If ticketless mode:** Skip this section.

**If ticket mode:** Before starting the pipeline, add the "Working" label to signal work in progress:
```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Working"
```

## Pipeline

This pipeline has 9 phases. Execute them in order. Between major phases, report progress to the user. **Read each phase file only when you reach that phase** — do not read all files upfront.

**Hard stop after planning**: Phases 2–9 run only when the skill was invoked with a plan-file argument (`hasPlanFile` set during mode detection). A session that creates a new plan **always ends at Phase 1** — after persisting the plan file, do not read `phases/phase-2-worktree.md` or any later phase file. Implementation resumes via `/ccflow:implement .plans/<filename>` in a fresh session.

| Phase | Instructions |
|-------|--------------|
| 1 | `phases/phase-1-plan.md` |
| 2 | `phases/phase-2-worktree.md` |
| 3 | `phases/phase-3-test-red.md` |
| 4 | `phases/phase-4-implement-green.md` |
| 5 | `phases/phase-5-refactor.md` |
| 6 + 7 | `phases/phase-6-7-review.md` |
| 8 | `phases/phase-8-docs.md` |
| 9 | `phases/phase-9-pr.md` |

The detailed instructions for each phase live in `phases/`. Read only the file for the phase you are starting; do not pre-read later phase files.

### Cost Controls

Read `.claude/config.json` for optional `ccflow` settings:

- `ccflow.compactImplementation: true` — for small, low-risk plans only, Phase 3, 4, and 5 may be handled by a single implementer delegation. The implementer must still explicitly report red test failures, green implementation, refactoring, and final build/test results. Do not use this mode for security-sensitive, data migration, auth/payment, large UI, or unclear-requirement work.
- `ccflow.reviewConcurrency: "sequential"` — run the same Phase 6 + 7 reviewers one after another instead of in parallel. Quality gates are unchanged; this only smooths usage limits. Default is `"parallel"`.
- `ccflow.diffContextMode: "file"` — before Phase 6 + 7, write the full diff to `/tmp/claude/ccflow-diff.patch` and pass reviewers the path plus changed file list and stat. Reviewers read only the hunks they need. Default is `"inline"` for small diffs.

If a cost-control setting conflicts with quality, ignore the setting and explain why.

Proceed to Phase 1 by reading `phases/phase-1-plan.md`.
