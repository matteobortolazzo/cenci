---
name: design
description: "Run interactive design reasoning and create .pen files using Pencil."
compatibility: Requires Claude Code interactive gates and the configured Pencil integration.
argument-hint: <ticket-id | design description> [additional context]
user-invocable: true
disable-model-invocation: true
model: opus
allowed-tools: Read, Write, Bash(pen), Bash(pen interactive:*), Bash(which pen:*), Bash(gh issue view:*), Bash(gh issue edit:*), Bash(gh issue comment:*), Bash(gh issue list:*), Bash(gh issue close:*), Bash(gh label create:*), Bash(gh api user --jq:*), Bash(git remote get-url:*), Bash(git add:*), Bash(git commit:*), Bash(git rev-parse:*), Bash(mkdir:*), Bash(mktemp -d:*), Bash(test:*), Glob, Grep, AskUserQuestion, mcp__pencil__get_app_state, mcp__pencil__execute, mcp__pencil__get_screenshot, mcp__pencil__export_nodes, mcp__pencil__get_guidelines
---

> **Client dispatch**: In Codex, read `codex-runtime` and `design/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

> **Pencil API reference**: Before any Pencil call in this skill, in either `cli-app` or `editor` mode, read the `pencil-api` reference skill — it is the single source of truth for the current MCP tool surface, the `execute` idiom catalog, the transport table, and document discipline every Pencil call site below relies on.

<!-- Architecture note: cenci orchestrates Pencil via `pen interactive` CLI (cenci-driven model).
     We do NOT use `pen --agent-config` because:
     1. cenci needs ticket/worktree/approval workflow integration that agent-config agents lack
     2. agent-config agents have no cenci context (config, CLAUDE.md, docs/)
     3. For complex designs, we batch via multiple `execute` calls within one session
     The Pencil editor is the design engine; Claude Code drives it via CLI subprocess (or MCP as legacy fallback).
     CLI mode (`pen interactive`, desktop-app-backed) avoids loading MCP tool schemas into every conversation,
     saving ~3,000-5,000 tokens per conversation and enabling command batching via heredocs. -->

## Phase 0 — Context Loading

Read `project-core` and resolve neutral configuration before continuing.

**Config check**: If neither canonical nor legacy configuration exists, **stop immediately** and tell the user:
"cenci is not configured for this project. Run `/cenci:configure` first to set up."

Use the config resolved by `project-core`.

**Pencil gating**: Check `pencil.enabled` in the resolved config. If absent or not true, **stop immediately** and tell the user:
"Pencil design workflows are not enabled for this project. Run `/cenci:configure` and enable Pencil when prompted."

Read `pencil.designPath` from the config to determine where design files belong. If the project is a monorepo with `pencil.shared: false`, determine the per-project `designPath` from the affected project's entry in the `projects` array.

## Pencil Communication Mode

Read `pencil.mode` from the resolved config and store as `$PENCIL_MODE`. Default: `"editor"` if absent.

**Convention**: All Pencil tool calls in this skill follow `$PENCIL_MODE`, per `pencil-api`'s transport table:

- **`"cli-app"`** (default for new installs): Execute tool calls via a `pen interactive` heredoc (targeting the desktop app — see Phase 0.5 below for the exact invocation) using the Bash tool. Multiple independent commands can be batched in a single heredoc.

  ```bash
  pen interactive -a desktop <<'EOF'
  execute({ input: '<Pencil-script>' })
  another_tool({ key: value })
  EOF
  ```

  Split into separate heredoc invocations at **decision boundaries** — where you need to read output before choosing the next action.

- **`"editor"`** (legacy MCP fallback): Call the equivalent `mcp__pencil__<tool>` MCP tool directly (e.g., `mcp__pencil__execute`). One tool call per invocation.

**Special cases in CLI mode**:

| Operation | CLI mode | Editor (MCP) mode |
|-----------|----------|-------------------|
| Screenshots | Use `export_nodes({ nodeIds: [...], outputDir: "<path>", format: "png" })` — writes to disk. Then Read the exported PNG with the Read tool. | Use `get_screenshot(nodeId)` — returns image inline. |
| Batch reads | Combine multiple `execute` script reads (see `pencil-api`'s idiom catalog) in one heredoc | One MCP call per tool |
| Batch writes | Combine multiple `execute` script writes in one heredoc (when independent) | One MCP call per tool |

When this skill says "Call `<tool_name>(...)`" or "run `<Pencil-script>`", execute it according to `$PENCIL_MODE` and the `execute` idiom catalog in `pencil-api`. Explicit CLI/MCP examples are only given where the modes diverge.

**Document-derived values** (node IDs, component names — read back out of the `.pen` document in Step 3B/3C, Step 4A, and Phase 5 Step A) must be validated per `pencil-api`'s idiom catalog before being substituted into any `execute` script below, in either mode — reject and abort the call site rather than interpolating an unvalidated value. This also covers node IDs interpolated into a filesystem path, such as Step 4A's `export_nodes`/`outputDir` and `Read` targets — see `pencil-api`'s narrower node-ID pattern for those sites.

## Phase 0.5 — Pencil Availability Check

**Sandbox guard (host-only)**: `/cenci:design` is host-only — the Pencil desktop app it drives is never reachable inside the cenci sandbox. In-sandbox sessions only get design access through headless reads via `/cenci:implement` and `verify-ui`, never through this skill.

Before doing anything else in this phase — before any Pencil probe, any background auto-launch of Pencil, and any retry, in both `cli-app` and `editor` mode below — detect an in-container session with the same two-step check as `configure/scripts/detect-project.sh`, each as its own Bash call:

1. `test "${CENCI_SANDBOX:-}" = "1"` — exit 0 → in container.
2. If step 1 exited non-zero, `test -f /.dockerenv` — exit 0 → in container.

**If either check matches**, stop immediately and tell the user:
"`/cenci:design` must run from a host session: the Pencil desktop app is not reachable from inside the cenci sandbox. Exit the container and re-run `/cenci:design <args>` on the host. Sandboxed sessions get design access through headless reads only (`/cenci:implement`, `verify-ui`)."
**Stop.**

If neither check matches, proceed with the normal availability check below.

Before parsing arguments, verify that Pencil is reachable. Both modes below use the cheap `execute({ input: 'Print(1)' })` connectivity probe from `pencil-api`'s idiom catalog — `get_app_state` is reserved for session orientation (Phase 2 onward), not spent here.

**CLI-app mode** (`$PENCIL_MODE` is `"cli-app"`):

1. Probe via Bash:
   ```bash
   pen interactive -a desktop <<'EOF'
   execute({ input: 'Print(1)' })
   EOF
   ```
2. **If the call succeeds** → Pencil is available. Proceed to argument parsing.
3. **If the call fails** → attempt auto-launch:
   a. Launch Pencil in the background: run the bare command `pen` with the Bash tool's background-execution mode (the equivalent of `pen &`). The command string must be exactly `pen` — never append `&` to it, or the invocation stops matching the exact-match `Bash(pen)` grant and prompts. Then retry the probe up to 3 times with 3-second pauses between attempts.
      - If a retry succeeds → proceed to argument parsing.
      - If all 3 retries fail → tell the user:
        "Could not reach Pencil. Open the Pencil desktop app manually and ensure it's accepting CLI connections, then re-run `/cenci:design`."
        **Stop.**

**Editor mode** (`$PENCIL_MODE` is `"editor"`):

1. Call `mcp__pencil__execute` with `{ input: 'Print(1)' }` as an MCP connectivity probe.
2. **If the call succeeds** → Pencil MCP is available. Proceed to argument parsing.
3. **If the call fails** → attempt auto-launch:
   a. Run `which pen 2>/dev/null` to check if the `pen` command is available.
   b. **If found**: Launch Pencil in the background: run the bare command `pen` with the Bash tool's background-execution mode (the equivalent of `pen &`). The command string must be exactly `pen` — never append `&`. Then retry the `execute({ input: 'Print(1)' })` probe up to 3 times with 3-second pauses between attempts.
      - If a retry succeeds → proceed to argument parsing.
      - If all 3 retries fail → tell the user:
        "Could not reach Pencil. Open the Pencil desktop app manually, then re-run `/cenci:design`. Check MCP server status in Pencil (View → MCP Server Status) and ensure the Pencil MCP server is listed in your Claude Code MCP configuration."
        **Stop.**
   c. **If not found**: Tell the user:
      "The Pencil editor is not running and the `pen` command is not in PATH. Either:
      1. Open Pencil manually and ensure its MCP server is connected, or
      2. Install the `pen` command (`npm install -g @pen.dev/cli`, or from within the Pencil app: File → Install `pen` command into PATH) for auto-launch support."
      **Stop.**

**Parse `$ARGUMENTS` — Mode Detection:**

Extract the first whitespace-delimited token from `$ARGUMENTS` and determine the mode:

- **If the first token matches `^\d+$` or `^#\d+$`** → **ticket mode**
  - Strip any `#` prefix to get the numeric ticket ID.
  - Everything after the first token is optional **user context** (additional instructions or focus areas).
  - Examples: `#1 focus on layout` → ID `1`, context `focus on layout`; `7` → ID `7`, no context.

- **Otherwise** → **ticketless mode**
  - The entire `$ARGUMENTS` string is the **design description**.
  - There is no ticket ID — the design description is the primary input.

**If ticket mode:** Fetch the ticket:

**Shell rules**: Read the `shell-rules` skill before running any `gh` commands (covers heredoc temp-file pattern).

Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`), then run:
```bash
gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,comments
```

**Dedicated-ticket check:** if the fetched ticket does **not** carry the `Design` label, note that the workflow expects design work on a dedicated design ticket (created by `/cenci:refine` as a companion or design-first split child). Ask via `AskUserQuestion`:

> "This ticket isn't labeled `Design` — the workflow expects design on a dedicated design ticket so the implementation ticket stays separate. Design directly on this ticket anyway?"

- **"Stop — create a design ticket first (Recommended)"** — stop. Tell the user to re-run `/cenci:refine <ticket-id>` (which creates the companion design ticket) or create one manually with the `Design` label.
- **"Proceed on this ticket"** — continue; the ticket will be labeled `Designed` but not closed (legacy mixed flow).

Read the `ticket-ownership` reference skill and follow it using the assignees from
the ticket fetch above. Complete its claim-and-verify contract before attachments,
design reasoning, or adding `Working`. Never replace an existing assignee.

Read the ticket body and look for a **Design Direction** section (produced by `/cenci:refine` for frontend tickets). Store it for use in Phase 2.

**If ticketless mode:** Skip ticket fetching. The design description from `$ARGUMENTS` is the primary input.

Read any relevant `docs/<topic>.md` files for entries related to design or this feature area. If a legacy `.claude/rules/lessons-learned.md` exists in the project, read it as fallback.

## Phase 1 — Attachments

**If ticketless mode:** Skip this section entirely and proceed to Phase 2.

**If ticket mode:** Read the `attachments` reference skill and follow its 4-step procedure to discover, present, download, and load ticket attachments. If no attachments are found or the user selects none, proceed to Phase 2.

## Phase 2 — Design Understanding

This is the forced reasoning phase. Do not create or modify any `.pen` files yet.

### Label "Working" (at start)

**If ticketless mode:** Skip this.

**If ticket mode:** Before starting design work (at the beginning of Phase 2), add the "Working" label. `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure it exists first — each as its own Bash call (`|| true` swallows only the "already exists" error):
```bash
gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "Actively being refined, designed, or implemented" 2>/dev/null || true
```
```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Working"
```

### Step 2A: Classify Design Type

Based on the ticket description (or design description in ticketless mode), classify what needs designing:

| Type | Examples |
|------|----------|
| **screen/page** | Settings page, profile page, checkout flow |
| **component** | Date picker, card, notification banner |
| **dashboard** | Analytics dashboard, admin panel |
| **landing-page** | Marketing page, product page, hero section |
| **form/wizard** | Multi-step form, signup wizard, onboarding |
| **slides/presentation** | Pitch deck, project update, onboarding slides |

### Step 2B: Retrieve Pencil Guidelines

Call `get_guidelines` with the topic most relevant to the classification:

| Design Type | Guideline Topic |
|-------------|----------------|
| landing-page | `landing-page` |
| dashboard, screen/page, form/wizard | `design-system` |
| component | `design-system` |
| slides/presentation | `slides` |

### Step 2C: Get Style Inspiration

1. Call `get_guidelines({ category: "style" })` to list available styles
2. Select the style that best matches the design task based on classification and context
3. Call `get_guidelines({ category: "style", name: "<selected-style>" })` to load the full style definition (pass any required `params` if the style requests them)

### Step 2D: Iterative Propose-First Questioning

Ask questions one at a time using `AskUserQuestion`. Propose specific answers rather than asking open-ended questions. Limit to 3–5 questions total. Skip any question already answered by the ticket's Design Direction section.

**Question 1 — Scope validation:**
> "Based on [the ticket / your description], I'll design [specific thing] containing [proposed elements]. Does this match your expectations?"

Options: "Yes, that's right", "Adjust scope" (+ description field)

**Question 2 — Design system discovery:**

- If the user specified a `.pen` file path in `$ARGUMENTS`, skip scanning and use that file directly.
- Otherwise, first check the configured `designPath` for existing `.pen` files using Glob (`<designPath>/*.pen`).
- If no `.pen` files found in `designPath`, fall back to a repo-wide scan: Glob (`**/*.pen`).

Then:
- If **no `.pen` files found** → designing from scratch. Mention this to the user.
- If **exactly one `.pen` file found** → propose using it: "Found existing design file `<path>`. Should I use its components as the design system?"
- If **multiple `.pen` files found** → present via `AskUserQuestion`:
  > "Found N design files. Which should I use as the design system (or start fresh)?"
  Options: one per `.pen` file path, plus "Start fresh (no design system)"

If using an existing `.pen` file, verify document identity before reading it — no tool creates or opens documents; opening or switching the active document in Pencil is always a human action (see `pencil-api`'s document discipline section). Call `get_app_state` and compare its active document path against the target `.pen` file:
- **If it already matches** → proceed directly to reading its reusable components with `Get(n=>n.reusable&&Print(n.id,n.name))` (per `pencil-api`'s idiom catalog).
- **If it does not match** (including no document open) → ask the user via `AskUserQuestion`:
  > "Please open `<target .pen file>` in Pencil (File → Open), then confirm here."
  Options: "Done, file is open", "Cancel"
  - If **"Done"** → call `get_app_state` again to confirm the correct file is now the active document. If it still doesn't match, ask again once more with the same options; if it still mismatches after that second attempt, treat it as **"Cancel"**. Once confirmed, read its reusable components with `Get(n=>n.reusable&&Print(n.id,n.name))`.
  - If **"Cancel"** → skip design system loading and proceed as if designing from scratch.

**Question 3 — Visual direction:**

If the ticket has a **Design Direction** section from `/cenci:refine`, propose using it:
> "The ticket specifies this design direction: [summary]. I'll follow this. Any adjustments?"

If no Design Direction exists, propose a direction from the style guide:
> "Based on the style guide, I'd suggest [specific aesthetic tone, e.g., 'editorial with high-contrast typography and generous whitespace']. Does this work, or do you have a different direction?"

Options: "Use this direction", "Different direction" (+ description field)

**Question 4 — Screen states** (conditional — only for screens/pages/forms):
> "Which states should I design? I'd suggest [empty, populated, error] at minimum."

Options (multiSelect=true): "Empty state", "Populated / default", "Error state", "Loading state"

**Question 5 — Responsive** (conditional — only for screens/pages/landing pages):
> "Desktop only, or should I also design for mobile/tablet?"

Options: "Desktop only", "Desktop + Mobile", "Desktop + Tablet + Mobile"

## Phase 2.5 — Prepare Design Directory

After all design questions are answered, ensure the design directory exists. Design work runs directly on the current branch (expected: `main`) — **no feature branch is created**. Pencil keeps the `.pen` file open across invocations, and branch switching forces the user to re-open it manually in Pencil.

```bash
mkdir -p <designPath>
```

## Phase 3 — Design Creation

Now create the design using Pencil tools. **All file paths in this phase must be absolute paths** within the repository root.

### Step 3A: Verify Document Identity

No tool in the current Pencil API creates or opens documents — opening a design-system file or creating a new one is always a human action in the Pencil app (see `pencil-api`'s document discipline section). This step verifies the active document matches what Phase 2/2.5 selected before any read or write in this phase begins.

Determine the **target file**:
- If a design system `.pen` file was selected in Phase 2 → the target is that file's absolute path.
- If designing from scratch → the target is a new `.pen` file the human will create directly inside `<designPath>` (see the from-scratch branch below).

**If a design system file was selected:**
- Call `get_app_state` and compare its active document path against the target.
- **Match** → proceed to Step 3B.
- **Mismatch** (including no document open) → ask the user via `AskUserQuestion`:
  > "Pencil's active document doesn't match `<target file>`. Please open it in Pencil (File → Open), then confirm here."
  Options: "Done, ready to proceed", "Cancel design"
  - If **"Done"** → call `get_app_state` again. If it now matches, proceed to Step 3B. If it still mismatches, ask once more with the same options; if it is still mismatched after that second attempt, **Stop** and tell the user design cannot continue without the correct document open. Never proceed to Step 3B on a mismatch.
  - If **"Cancel design"** → **Stop.**

**If designing from scratch:**
- Before prompting, establish the baseline `.pen` file set for `<designPath>`: reuse the `Glob` (`<designPath>/*.pen`) result already captured during Phase 2's Question 2 discovery scan — nothing in Phase 2.5 (`mkdir -p <designPath>`) adds `.pen` files, so that result is still an accurate baseline. This baseline matters even when it's non-empty: the from-scratch branch is also reached after the user cancels design-system loading in Phase 2/Step 3A, at which point `<designPath>` can already contain other, pre-existing `.pen` files. If no such Glob result exists yet (the user-specified-path route in Phase 2's Question 2 skipped its discovery scan entirely), run `Glob` (`<designPath>/*.pen`) now, before prompting the human to create the new document, so a baseline exists to diff against.
- Ask the user via `AskUserQuestion`:
  > "Please create and save a new `.pen` file inside `<designPath>` in Pencil, then confirm here."
  Options: "Done, file created and saved", "Cancel design"
  - If **"Cancel design"** → **Stop.**
  - If **"Done, file created and saved"** → re-run `Glob` (`<designPath>/*.pen`) and diff it against the baseline set captured above to find files present now that were **absent** from the baseline:
    - **Exactly one new file** → this is the target. Call `get_app_state` and confirm the new file is the active document — on mismatch, apply the same re-ask-once-then-stop handling as the design-system-file branch above. Never proceed to Step 3B on a mismatch.
    - **No new file, or more than one new file (ambiguous)** → ask the user via `AskUserQuestion` to clarify which file is the new document (or confirm it was actually saved, if none appeared). Never guess by picking the first or most-recently-modified match. Once the user identifies a specific file, call `get_app_state` and confirm that file is the active document before proceeding to Step 3B — on mismatch, apply the same re-ask-once-then-stop handling as the design-system-file branch above.

**Important (both modes)**: no tool in the current Pencil API surface (`pencil-api`'s MCP surface table) accepts a caller-supplied `filePath` parameter to target a specific document — every call always operates on whatever document is currently open in the app. There is no per-call way to redirect a wayward call to the correct document; identity is established purely by what's open when the call runs, which is why it must be verified with `get_app_state` before every read/write batch rather than assumed from the check above (see `pencil-api`'s Document Discipline section).

### Step 3B: Inventory the Document

Call `get_app_state({ include_schema: true })` (per `pencil-api`'s MCP surface) to load the full `.pen` schema and `execute` function docs needed for component discovery and later building, alongside session orientation. This call runs unconditionally here, in both the design-system and from-scratch branches — Step 3D's build step needs the schema and `execute` docs either way, not just when a design system file was selected.

Then run the top-level inventory idiom from `pencil-api`'s catalog to understand the document's existing structure before building:

```
Get((n,c)=>{c.skipChildren();Print(n.id,n.name)})
```

### Step 3C: Load Design System Components

If a design system file was selected:
- Run the reusable discovery idiom, `Get(n=>n.reusable&&Print(n.id,n.name))`, to discover all reusable components. For any component whose full subtree is needed, follow up with the subtree-read idiom, `Print(Get("<id>",{depth:3}))`.
- Catalog available components (buttons, inputs, cards, navigation, etc.) for use in the design

### Step 3D: Build the Design

Use `execute` calls to create the design. Follow these rules:

- Split larger designs into multiple `execute` calls by logical section (e.g., header first, then content area, then footer)
- Use reusable components from the design system where available — insert with the ref-insert idiom, `Insert("<parent>",{type:"ref",ref:"<Comp>",width:"fill_container"})`
- For new elements not in the design system, create frames and text nodes directly
- Apply styling from the style guide and Design Direction
- Run `FindEmptySpace` when positioning new screens to avoid overlapping existing content
- Generate images with `Generate(nodeId,"ai"|"stock",…)` where needed (hero images, avatars, illustrations)
- Set theme variables via `SetVariables({...})` if creating a new design system or extending an existing one — always a merge, **never** pass `replace: true`, which would destroy the existing theming instead of extending it
- Use absolute positioning within flex layouts for floating elements (FABs, modals, overlays, tooltips)

**Build order:**
1. Create the screen/page frame with overall layout
2. Add structural sections (header, sidebar, content area, footer)
3. Populate each section with components and content
4. Apply typography, colors, spacing, and other styling
5. Add images and decorative elements
6. Create additional screen states if requested (empty, error, loading)

### Step 3E: Responsive Variants

If the user requested responsive designs:
1. Find empty space on the canvas to the right of the desktop design
2. Create mobile (375px wide) and/or tablet (768px wide) variants
3. Adapt the layout for each breakpoint (stack columns, resize elements, hide secondary content)

## Phase 4 — Visual Validation Loop

### Step 4A: Screenshot and Inspect

**Re-verify document identity before this batch**: multiple `AskUserQuestion` round-trips happen between Step 3A and here, during which the human could switch Pencil's active document. Call `get_app_state` and compare its active document path against the target file established in Step 3A. Apply the same re-ask-once-then-Stop handling as Step 3A — never proceed to a screenshot or read/write below on a still-mismatched document.

For each screen/component created:

1. Capture a visual snapshot:
   - **CLI mode**: Call `export_nodes` to save screenshots to a scratch directory **outside the repo**, then Read the exported PNG. These are local validation artifacts only — never committed.

     Create the screenshot directory **once**, on first entry into this step, as its own standalone Bash call — never a shell-variable assignment that captures the command's own output, and never any other form of command substitution: `shell-rules` forbids command substitution where the agent can run the command and read the result, and an assignment wrapper would not match the granted `Bash(mktemp -d:*)` prefix.
     ```bash
     mktemp -d "${TMPDIR:-/tmp}/cenci-design-XXXXXX"
     ```
     Verify the printed path with its own standalone Bash call before using it:
     ```bash
     test -d "<printed-path>"
     ```
     If this verification fails, stop the step immediately using the same error-recovery convention as Phase 0/0.5 — do not proceed to `export_nodes` with an unverified directory.

     `<node-id>` below is a document-derived value — validate it against `pencil-api`'s narrower node-ID allowlist pattern before interpolating it, since it lands in a filesystem path (`outputDir`, the `Read` target) as well as the `execute` script; reject and abort rather than interpolating an unvalidated ID.

     Substitute the **printed literal path** (never an unsubstituted shell-variable reference — a quoted `<<'EOF'` heredoc never expands shell variables, so leaving one in place would reach `export_nodes` as literal, wrong text instead of the resolved path) into the heredoc's `outputDir`, the subsequent `Read(...)` call, and Step 4B's re-screenshot loop — reuse the same printed path for every screenshot taken during this design session, in both this step and Step 4B:
     ```bash
     pen interactive -a desktop <<'EOF'
     export_nodes({ nodeIds: ["<node-id>"], outputDir: "<screenshot-dir>", format: "png" })
     execute({ input: 'Get("<node-id>",(n,c)=>c.problems&&Print(n.name,c.problems))' })
     EOF
     ```
     Then: `Read("<screenshot-dir>/<node-id>.png")` to view and analyze.
   - **Editor mode**: Call `get_screenshot(nodeId)` to receive the image inline.
2. **Analyze the screenshot** for:
   - Alignment issues (elements not lined up properly)
   - Readability problems (text too small, low contrast)
   - Visual hierarchy (clear headings, proper spacing, content grouping)
   - Completeness (all specified elements present)
   - Clipping (content cut off or overflowing)
3. Review the layout-check `Get(...,(n,c)=>c.problems&&...)` output (captured alongside the screenshot) for programmatic layout problems
4. Fix any issues found via additional `execute` calls
5. Re-screenshot after fixes to confirm they resolved the problems

### Step 4B: Present to User

After validation passes, present the design to the user via `AskUserQuestion`:

> "Here's the design for [description]. I've verified alignment, readability, and completeness. What do you think?"

Options:
- **"Approve"** — proceed to Phase 5
- **"Request Changes"** — describe what to change
- **"Start Over"** — redesign from scratch

If **"Request Changes"**:
1. Ask what needs changing (via `AskUserQuestion` if the user didn't specify inline)
2. **Re-verify document identity before this batch**: the Approve/Request-Changes prompt is another `AskUserQuestion` round-trip during which the human could switch Pencil's active document. Call `get_app_state` and compare its active document path against the target file established in Step 3A. Apply the same re-ask-once-then-Stop handling as Step 3A — never proceed to the `execute` calls below on a still-mismatched document.
3. Apply the requested changes via additional `execute` calls
4. Re-screenshot and re-validate (loop back to Step 4A)
5. Re-present the updated design (back to Step 4B)

If **"Start Over"**:
1. **Re-verify document identity before this batch**: the Approve/Request-Changes prompt is another `AskUserQuestion` round-trip during which the human could switch Pencil's active document. Call `get_app_state` and compare its active document path against the target file established in Step 3A. Apply the same re-ask-once-then-Stop handling as Step 3A — never delete anything from the canvas on a still-mismatched document.
2. Delete the current design from the canvas
3. Loop back to Phase 2, Question 3 (visual direction) to take a new direction
4. Rebuild from Phase 3

**Only proceed to Phase 5 after the user selects "Approve".**

## Phase 5 — Generate DESIGN.md

After the user approves the design in Phase 4, generate a `DESIGN.md` spec that documents the design for implementation.

### Step A: Extract data from .pen file

**Re-verify document identity before this batch**: the Phase 4 approval round-trip happens between Step 3A/4A and here, during which the human could switch Pencil's active document. Call `get_app_state` and compare its active document path against the target file established in Step 3A. Apply the same re-ask-once-then-Stop handling as Step 3A — never proceed to the reads below on a still-mismatched document.

In CLI mode, batch all reads into a single invocation, using the `execute` idioms from `pencil-api`'s catalog:

```bash
pen interactive -a desktop <<'EOF'
execute({ input: 'Get((n,c)=>{c.skipChildren();/^Screen\\//.test(n.name)&&Print(n.id,n.name)})' })
execute({ input: 'Get(n=>n.reusable&&Print(n.id,n.name))' })
execute({ input: 'Get((n,c)=>{c.skipChildren();/^Note:/.test(n.name)&&Print(n.id,n.name)})' })
execute({ input: 'Print(GetVariables())' })
EOF
```

In editor mode, call `mcp__pencil__execute` once per script above via MCP — but not with the same literal string. The `\\/` in `/^Screen\\//` above is escaped for the CLI heredoc form's own string-literal-parsing layer inside `pen interactive`; passed through MCP there is no such extra layer, so the `\\` would survive as a literal backslash and break the regex. In editor mode, use the un-escaped form instead: `mcp__pencil__execute` with `{ input: 'Get((n,c)=>{c.skipChildren();/^Screen\//.test(n.name)&&Print(n.id,n.name)})' }`.

Parse the output into:

1. **Screens**: From the `Screen/.*` results — extract name, node ID. Derive route from the screen name (e.g., `Screen/training-plan` → `/training-plan`). Add a brief description based on the screen content.
2. **Components**: From the reusable-node results — extract name, node ID. Derive the framework component name from the Pencil component name (e.g., `Component/ExerciseCard` → `ExerciseCardComponent` for Angular, `ExerciseCard` for React). Determine UI library usage (e.g., PrimeNG, Material UI, custom) from component structure. Note which screens use each component.
3. **Annotations**: From the `Note:.*` results — extract name, node ID, and topic from the note content.
4. **Tokens**: From `GetVariables()` — categorize variables into Colors, Typography, Radii, and Spacing. Map each to a CSS custom property name (e.g., `$bg-card` → `--bg-card`).

### Step B: Detect framework from config

Read `stack.frontend` (or per-project stack) from the resolved config to determine:
- Column headers for the Components table (Angular, React, Vue, or Generic)
- Component naming conventions (e.g., `<Name>Component` for Angular, `<Name>` for React)

### Step C: Write DESIGN.md

Use the template at `${CLAUDE_PLUGIN_ROOT}/templates/design-spec.md` as the base. Fill all parameterized sections with extracted data:
- Replace `<design-path>` with the configured `designPath`
- Replace `<pen-file-name>` with the actual `.pen` file name
- Replace `<framework>` and select the matching Components table variant
- Populate Screens, Components, Annotations, and Design Tokens tables with extracted data
- Write the completed file to `<designPath>/DESIGN.md`

**If `DESIGN.md` already exists** at that path, ask the user via `AskUserQuestion`:
> "A DESIGN.md already exists at `<designPath>/DESIGN.md`. What should I do?"

Options: "Overwrite with new spec", "Merge (add new entries, keep existing)"

- **Overwrite**: Replace the file entirely.
- **Merge**: Read the existing file, add new screens/components/tokens that don't already exist, preserve existing entries.

## Phase 6 — Report Summary

### Report

Summarize what was created:
- `.pen` file path(s) created or modified
- Screens/components designed (list each with a brief description)
- Key design decisions (aesthetic tone, color palette, typography, layout approach)
- Design system components used (if any)
- `DESIGN.md` path, screen count, component count, token count

Include this note at the end of the report:
> "Note: Pencil does not auto-save. Save the `.pen` file manually in Pencil (Cmd/Ctrl+S) before closing — unsaved `execute` changes will be lost. The design file remains open for your review."

## Phase 7 — Commit Design

After Phase 6 reporting is complete, commit the design artifacts on the current branch. **No branch switch, no push, no PR.**

### Step 7.0: Manual Save Reminder (REQUIRED)

**Pencil does NOT auto-save `.pen` files.** Changes made via `execute` exist only in the open editor until the user manually saves. `git add` reads from disk, so committing without saving captures a stale `.pen` file.

Before proceeding, prompt the user via `AskUserQuestion`:
> "Pencil does not auto-save. Please save the `.pen` file in Pencil now (File → Save or Cmd/Ctrl+S), then confirm."

Options: "Saved, proceed", "Cancel commit"

- **"Saved, proceed"** → re-verify document identity before continuing: call `get_app_state` and compare its active document path against the target file established in Step 3A. Apply the same re-ask-once-then-Stop handling as Step 3A — never proceed to Step 7A's `git add` on a still-mismatched document. Once confirmed, continue to Step 7A.
- **"Cancel commit"** → **Stop.** Do not commit.

### Step 7A: Commit

Stage and commit the design artifacts on the current branch. **Do not stage screenshots** — the `.pen` file is the source of truth and any `screenshots/` directory inside `<designPath>` is local-only scratch.

Stage and commit as **two standalone Bash calls** — never a `&&` compound (per `shell-rules`, Claude Code evaluates every segment of a compound, so a compound can prompt even when both halves are granted).

```bash
git add <designPath>/*.pen <designPath>/DESIGN.md
```

Verify both files were actually staged before committing, using only the client's file tools (e.g. Glob or Read — no additional Bash grant needed): confirm the `.pen` file exists at `<designPath>` so the `git add` glob above is known to have matched it, rather than silently resolving to nothing. If it's missing, stop and apply the same recovery as Step 7D's "Commit fails" case rather than committing an incomplete change.

```bash
git commit -m "feat(design): <description>" -- <designPath>/*.pen <designPath>/DESIGN.md
```

- **If ticket mode:** Include ticket ref in the commit body: `#<ticket-id>`
- **If ticketless mode:** Use the design description slug in the commit message

### Step 7B: Post Design Comment

**If ticketless mode:** Skip this step.

**If ticket mode:** Post the design reference and key decisions as a ticket comment — this keeps the ticket body owned by its author while still surfacing the design context for humans and for the context-gatherer (which bundles ticket comments during planning).

1. Get the commit SHA: `git rev-parse HEAD`
2. Write the comment body with the client's file tool (per `shell-rules` — do not use a shell heredoc) to a temp file, e.g. `${TMPDIR:-/tmp}/cenci/design-comment-<number>.md`. Every posted design comment opens with the cenci attribution banner (blockquoted) and carries the `<!-- cenci-design-summary -->` marker on its own non-blockquoted line (#951 — see `docs/comment-attribution.md`). Include a `### Screen nodes` subsection listing each screen extracted in Phase 5 Step A by name and node ID; omit the subsection entirely when there are no `Screen/*` nodes (e.g. a component-only or from-scratch design with nothing to list). The same document-derived-value validation rule from `pencil-api`'s `execute` Idiom Catalog section also applies here — validate each screen name and node ID against the allowlist (the name pattern for screen names, the narrower node-ID pattern for node IDs) before including it in this posted comment; reject rather than post an unvalidated value:
   ```markdown
   > 🤖 **cenci** — design summary posted by `/cenci:design` (design handoff).

   ### Design Reference
   - Design file: `<designPath>/<pen-file-name>`
   - Design spec: `<designPath>/DESIGN.md`
   - Commit: `<commit-sha>`

   ### Design Decisions
   - Aesthetic tone: <from Phase 6 report>
   - Color palette: <from Phase 6 report>
   - Typography: <from Phase 6 report>
   - Layout approach: <from Phase 6 report>

   ### Screen nodes
   - `<screen-name>` — `<node-id>`
   - `<screen-name>` — `<node-id>`

   <!-- cenci-design-summary -->
   ```
3. Post it:
   ```bash
   gh issue comment <number> --repo <owner>/<repo> --body-file "${TMPDIR:-/tmp}/cenci/design-comment-<number>.md"
   ```

### Step 7C: Label Ticket

**If ticketless mode:** Skip labeling.

**If ticket mode:** Replace "Working" with "Designed". Ensure the label exists first (same self-heal pattern as Working — `gh issue edit --add-label` fails on a missing label):
```bash
gh label create "Designed" --repo <owner>/<repo> --color "5319E7" --description "Design spec approved" 2>/dev/null || true
```
```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Designed" --remove-label "Working"
```

**If the ticket carries the "Design" label** (a design-only ticket from `/cenci:refine` — the design spec *is* the deliverable, no implementation follows), additionally:

1. **Find the implementation tickets this design blocks.** Union of:
   - The design ticket's own native **Blocking** set — the authoritative source, the exact inverse of the `--add-blocked-by` link `/cenci:refine` applies (requires `gh` >= 2.94.0):
     ```bash
     gh issue view <number> --repo <owner>/<repo> --json blocking --jq '.blocking.nodes[].number'
     ```
   - Any `Blocks #<n>` lines in the design ticket's body
   - Open issues whose body carries the supplementary prose dependency line (`Depends on #<number>` — a permanent, human-visible line `/cenci:refine` writes alongside the native link on every refined ticket that actually has a dependency, i.e. a blocking sibling or a companion design ticket; never a transitional form):
     ```bash
     gh issue list --repo <owner>/<repo> --state open --search "\"Depends on #<number>\" in:body" --json number,title
     ```
   Deduplicate the three lists.

   There is no `blocked-by:` search qualifier, so the native lookup is a direct read of this ticket's own `blocking` field rather than a repo-wide search — which is both cheaper and exact, since it cannot miss a dependent whose body never mentioned the design ticket.

2. **Propagate `Designed` to each dependent.** This is what satisfies implement's Design gate — the gate checks the label on the ticket being implemented, not the ticket body:
   ```bash
   gh issue edit <dependent> --repo <owner>/<repo> --add-label "Designed"
   ```
   Also post the same comment from **Step 7B** on each dependent (reuse the temp file written there — same banner, same `<!-- cenci-design-summary -->` marker, one kind for both the ticket's own post and every dependent's, since it is literally the same file) via `gh issue comment <dependent> --repo <owner>/<repo> --body-file "${TMPDIR:-/tmp}/cenci/design-comment-<number>.md"`. Implement itself locates `DESIGN.md` via the configured `designPath` (see `implement/SKILL.md`), so this comment is for human/planning context — the context-gatherer bundles ticket comments when implement runs.

3. **Close the design ticket** — its deliverable is done:
   ```bash
   gh issue close <number> --repo <owner>/<repo> --comment "Design delivered: \`<designPath>/<pen-file-name>\` + \`<designPath>/DESIGN.md\`, committed on main. Propagated \`Designed\` to: <#list or 'none found'>."
   ```

If no dependents are found, still close the ticket — just note it in the closing comment so a missing `Depends on` link is visible.

### Step 7D: Error Recovery

- **Commit fails** → Display the `git add` / `git commit` commands, then use `AskUserQuestion` ("Ran them, continue" / "Skip") to confirm before proceeding. Do not retry automatically.
- **Design comment post fails** → Report the failure and continue; do not block on it.
- **Label update fails** → Report the failure and continue; do not block on it.

## After Commit

**STOP.** Do not:
- Enter plan mode or propose an implementation plan
- Offer to run `/cenci:implement` or start implementation
- Suggest next steps beyond telling the user to run `/cenci:implement` when ready

Final message:
- **Ticket mode (design-only ticket — has the "Design" label):** "Design committed on `main`. Ticket #<ticket-id> closed; `Designed` propagated to <#list>. Run `/cenci:implement <dependent-id>` when ready to implement."
- **Ticket mode (otherwise — legacy mixed flow):** "Design committed on `main`. Run `/cenci:implement <ticket-id>` when ready to implement."
- **Ticketless mode:** "Design committed on `main`."
