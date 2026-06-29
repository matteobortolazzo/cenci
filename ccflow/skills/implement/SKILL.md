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

1. **Do not read per-project CLAUDE.md files in the main agent.** The context-gatherer (see Context Gathering below) determines affected project(s) and bundles their CLAUDE.md content into the context bundle; pass it the `projects` array from config.
2. **Use project-specific commands**: When delegating to subagents, use the affected project's `buildCommand` and `testCommand` from config instead of inferring them globally (the digest names the affected projects).
3. **Point subagents at context, don't paste it**: When delegating to planner/implementer, pass the bundle path (or plan file path) for project context. Tell the subagent to read relevant `docs/<topic>.md` files (and the legacy `.claude/rules/lessons-learned.md` or `.claude/rules/lessons-learned-<slug>.md` if those legacy files exist). Do not pre-read those in the main agent.

### Design Context Loading

If `pencil.enabled` is `true` in `.claude/config.json`:

1. **Determine design path**: Read `pencil.designPath` from config. If the project is a monorepo with `pencil.shared: false`, pass all per-project `designPath` entries to the context-gatherer — it determines the affected project(s) and resolves which design path applies.
2. **Do not read or parse DESIGN.md in the main agent.** Pass the design path to the context-gatherer (see Context Gathering below), which loads DESIGN.md, parses screen/component node IDs and design tokens, and writes them into the bundle's `## Design Context` section. The digest reports whether a design was found and the `.pen` path. Phase 4 sources `designScreenIds`, `designComponentMap`, and `designTokens` from the plan file's `## Design Context` section. Do not read the `.pen` file — subagents cannot use Pencil tools, so `.pen` content must be pre-read by the main agent only when needed (Phase 4).
3. **Pencil availability probe**: Read `pencil.mode` from config (default: `"editor"`). Store as `$PENCIL_MODE`. Before any Pencil calls later in the pipeline, attempt a lightweight probe:

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

**Shell rules**: Read the `shell-rules` skill before generating any shell in this pipeline — it covers the heredoc temp-file pattern, zsh-safe portability (no bash associative arrays), and the rule against `cd <dir> && …` compounds and hand-rescuing stranded worktree edits.

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

**If ticket mode:** Do **not** fetch the ticket in the main agent (single exception: the stale-plan re-fetch in plan-file mode, Phase 1). Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`) for later commands; the ticket itself is fetched by the context-gatherer (see Context Gathering below) after the pre-flight check.

**If ticketless mode:** No ticket to fetch. The task description from `$ARGUMENTS` is the primary input.

## Pre-flight Check

### Settings Verification

**Before delegating to the context-gatherer (or before proceeding in ticketless mode)**, read `.claude/settings.json` and `.claude/config.json` and verify the required permissions are present. This check is mandatory before any context gathering: the gatherer runs read-only `gh` commands in a subagent, which is only safe once `Bash(gh *)` permission and `gh` authentication are verified here in the main agent.

1. Check `permissions.allow` in `.claude/settings.json` contains **at minimum**:
   - `Write(*)`
   - `Edit(*)`
   - `Read(~/.claude/plugins/**)`   (reads the pipeline's own phase files without prompting)
   - `Read(//tmp/claude*/**)`        (reads context bundles / diffs / scratchpad)
   - `Write(//tmp/claude*/**)`       (Write-tool temp files, e.g. context bundle)
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

## Context Gathering (Delegated)

Runs **after** the Pre-flight Check above — the settings verification and `gh auth status` check are the precondition that makes read-only `gh` safe inside a subagent (see the `subagent-safety` skill).

> **Blocking delegation — do not background or poll.** Invoke the `context-gatherer` as a single, foreground `Task` call and wait for its result inline. Do **not** run it in the background, do **not** announce that you'll "wait for it to complete," and do **not** call any monitoring/polling tool to check on it — the `Task` call returns the digest directly when the subagent finishes. The next pipeline step reads that returned digest; there is nothing to poll.

**If plan file mode** (`hasPlanFile = true`): skip this delegation entirely — the plan file already contains the bundled context, and `isChild`/`isLastChild`/`parentId` come from its front matter. The stale-plan re-fetch in Phase 1 (a single read-only `gh issue view`) is the explicit exception to the no-fetch rule and runs in the main agent after pre-flight.

**If ticket mode:** Delegate to the `context-gatherer` agent. Pass:

- The ticket number and `owner/repo`
- The bundle output path: `/tmp/claude/ccflow-context-<ticket-id>.md`
- Config facts: `claudeMdLocation`, `isMonorepo` and the `projects` array (if monorepo), and the design path (if `pencil.enabled`)

The gatherer fetches the ticket and comments, performs parent-child detection, discovers attachments, loads design and per-project context, writes the bundle file, and returns a compact digest. From the digest, store:

- `isChild`, `isLastChild`, `parentId` — for commit, PR body, and labeling
- `labels` — for the Ticket Readiness checks
- The attachment list — for the Attachments step
- `bundlePath` — passed to the planner and appended to the plan file in Phase 1

If the digest reports errors (ticket not found, auth failure), surface them to the user and stop. Do **not** re-fetch the ticket or re-read DESIGN.md in the main agent — the digest and bundle are the source of truth.

**If ticketless mode:** Delegate to the `context-gatherer` only when there is context to bundle (design enabled or monorepo), with the task description in place of the ticket and bundle path `/tmp/claude/ccflow-context-<slug>.md`. Otherwise skip — the task description is the entire input.

**Parent-child edge cases** (resolved inside the gatherer, recorded here for downstream phases):
- Parent already closed → `isLastChild = false` (skip auto-close)
- No `### Child Tickets` section on parent → gatherer uses the search fallback
- Some siblings manually closed → they don't count as open, don't block last-child detection

## Attachments

Runs after Context Gathering (which itself runs after the Pre-flight Check). The effective order is: mode detection → Pre-flight Check → Context Gathering → Attachments → Ticket Readiness.

**If ticketless mode:** Skip the Attachments section entirely.

**If plan file mode:** Skip — attachment summaries are already in the plan file.

**If ticket mode:** The context-gatherer digest already contains the discovered attachment list (Step 1 of the procedure). Read the `attachments` reference skill and follow Steps 2–4 (present, download, load) using that list. If the digest reports no attachments or the user selects none, proceed.

Store each attachment's file path for passing to subagents (subagents share the filesystem and can read attachments directly via `Read`).

## Ticket Readiness

**If ticketless mode:** Skip the Ticket Readiness check entirely and proceed to the Pipeline.

**If plan file mode:** Skip this check — readiness was verified when the plan was created.

**If ticket mode:** After context gathering, inspect the ticket's labels/tags before starting the pipeline:

Check the `labels` line from the context-gatherer digest.

If the ticket does **not** have a "Refined" label/tag, display a warning:
> "This ticket hasn't been refined yet. Consider running `/ccflow:refine <ticket-id>` first for better results. Do you want to proceed anyway?"

If the user says no → stop. If yes → proceed with the pipeline.

### Design Check (soft)

If the ticket is classified as frontend — its title or the digest summary mention UI components, pages, views, layouts, forms, modals, visual design, styling, CSS, animations, themes, or frontend frameworks (React, Angular, Vue, Svelte, etc.) — and does **not** have a "Designed" label/tag **and** the digest reports no design (`design: none`), display a suggestion:
> "This frontend ticket hasn't been designed yet. Consider running `/ccflow:design <ticket-id>` first for a visual reference. Do you want to proceed anyway?"

If the ticket lacks the "Designed" label but the digest reports a bundled `DESIGN.md`, skip the suggestion — the design spec is sufficient context.

If the user says no → stop. If yes → proceed with the pipeline. This is a soft-check — it never blocks implementation.

### Visual Check Reminder

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

This pipeline has 9 phases. Execute them in order without stopping for confirmation — the user pre-approved all phases, including commit, push, and PR creation, by invoking this skill. Between major phases, give a one-line status update and immediately continue to the next phase; do not wait for acknowledgment. **Read each phase file only when you reach that phase** — do not read all files upfront.

The only reasons to stop mid-pipeline are explicit error gates defined within individual phases (rebase conflicts, repeated build failures, push auth errors, unclear reviewer findings). If no error gate fires, complete all 9 phases. The pipeline is not complete until a PR URL has been created and returned to the user — never end with a status summary like "ready for PR" or "branch is ready."

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
