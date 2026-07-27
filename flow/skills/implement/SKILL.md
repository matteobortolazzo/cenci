---
name: implement
description: "Run the full cenci plan, test, implementation, review, and pull-request pipeline."
compatibility: Requires Claude Code subagents, interactive gates, hooks, slash commands, and plugin configuration.
argument-hint: <ticket-id | task description> [additional context]
user-invocable: true
disable-model-invocation: true
model: sonnet
allowed-tools: Read, Write, Edit, Bash, Glob, Grep, Task, AskUserQuestion, SlashCommand, mcp__context7, mcp__pencil__batch_get, mcp__pencil__get_variables, mcp__pencil__get_screenshot, mcp__pencil__snapshot_layout, mcp__pencil__get_editor_state
---

> **Client dispatch**: In Codex, read `codex-runtime` and `implement/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`. This also governs the `phases/*.md` files this skill invokes.

Read the `subagent-safety` reference skill before delegating work to subagents.

## Context

Read `project-core` and resolve neutral configuration before continuing.

Use the config returned by `project-core`. Shared guidance is `AGENTS.md`; read legacy
`CLAUDE.md` only as additional compatibility context.

> **Progressive disclosure**: Do NOT eagerly read reference docs in this Context section. The planner subagent reads relevant `docs/<topic>.md` files (and any legacy `.claude/rules/lessons-learned.md` if present) as part of its analysis. `docs/git-workflow.md` is only consulted in Phase 9 (commits/PRs). `.claude/rules/` is reserved for files explicitly `@`-imported by `CLAUDE.md`; do not assume anything lives there.

### Monorepo Context Loading

If `isMonorepo` is `true` in the resolved config:

1. **Do not read per-project AGENTS.md files in the main agent.** The context-gatherer determines affected projects and bundles their AGENTS.md content; pass it the `projects` array.
2. **Use project-specific commands**: When delegating to subagents, use the affected project's `buildCommand`, `testCommand`, and `lintCommand` (when set) from config instead of inferring them globally (the digest names the affected projects).
3. **Point subagents at context, don't paste it**: When delegating to planner/implementer, pass the bundle path (or plan file path) for project context. Tell the subagent to read relevant `docs/<topic>.md` files (and the legacy `.claude/rules/lessons-learned.md` or `.claude/rules/lessons-learned-<slug>.md` if those legacy files exist). Do not pre-read those in the main agent.

### Design Context Loading

If `pencil.enabled` is `true` in the resolved config:

1. **Determine design path**: Read `pencil.designPath` from config. If the project is a monorepo with `pencil.shared: false`, pass all per-project `designPath` entries to the context-gatherer — it determines the affected project(s) and resolves which design path applies.
2. **Do not read or parse DESIGN.md in the main agent.** Pass the design path to the context-gatherer (see Context Gathering below), which loads DESIGN.md, parses screen/component node IDs and design tokens, and writes them into the bundle's `## Design Context` section. The digest reports whether a design was found and the `.pen` path. Phase 4 sources `designScreenIds`, `designComponentMap`, and `designTokens` from the plan file's `## Design Context` section. Do not read the `.pen` file — subagents cannot use Pencil tools, so `.pen` content must be pre-read by the main agent only when needed (Phase 4).
3. **Pencil availability probe**: Read `pencil.mode` from config (default: `"editor"`). Store as `$PENCIL_MODE`. Before any Pencil calls later in the pipeline, attempt a lightweight probe:

   **CLI-app mode** (`pencil.mode` is `"cli-app"`):
   ```bash
   pencil interactive -a desktop <<'EOF'
   get_editor_state({ include_schema: false })
   EOF
   ```
   If it succeeds → set `pencilAvailable = true`. If it fails → run the **headless fallback probe** below.

   **Editor mode** (`pencil.mode` is `"editor"`):
   ```
   Call `get_editor_state()` via MCP — if it succeeds, set `pencilAvailable = true`.
   If it fails or times out, run the headless fallback probe below.
   ```

   **Headless fallback probe** (both modes): when the primary probe fails and the `pencil` binary is on `PATH` (`command -v pencil`), the CLI can still run the full editor engine with no GUI or desktop app — the normal situation inside the cenci sandbox, where the host's desktop editor and its MCP server are unreachable. Probe it:

   ```bash
   pencil interactive -o "${TMPDIR:-/tmp}/cenci-pencil-probe-$$.pen" <<'EOF'
   get_editor_state({ include_schema: false })
   EOF
   rm -f "${TMPDIR:-/tmp}/cenci-pencil-probe-$$.pen"
   ```

   If it succeeds → set `pencilAvailable = true` and `pencilHeadless = true` — Phase 4 design reads then run `pencil interactive` against the design `.pen` file directly instead of a desktop connection. If it fails too (binary missing, or no auth — headless mode needs a seeded `~/.pencil/session-cli.json` or a `PEN_CLI_KEY` env var) → set `pencilAvailable = false`.

   If every probe fails, inform the user: "Pencil unavailable — proceeding with DESIGN.md text content only. Open Pencil (or provide headless CLI auth via `pencil login` / `PEN_CLI_KEY`) and retry if live design reads are needed."
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
  - If the filename has a numeric prefix (`.plans/<id>-<slug>.md`, ticket mode), extract that ticket ID and defer entirely to the **Plan Verification** step below — it validates and resumes from the file; do not read or parse it yourself here.
  - If the filename has no numeric prefix (`.plans/<slug>.md`, a ticketless-mode plan — **Plan Verification** below operates on ticket IDs and does not apply), `Read` the plan file directly, parse its YAML front matter (between `---` delimiters) for `mode` and `slug`, set `hasPlanFile = true`, and **validate** the slug with the same check and hard-stop as the freshly-generated case below — a hand-edited or pre-validation plan file can carry a slug that was never checked, and it is just as load-bearing on this path.
  - The rest of `$ARGUMENTS` after the file path is ignored.

- **Otherwise** → **ticketless mode**
  - The entire `$ARGUMENTS` string is the **task description**.
  - Generate a **slug** from the description: take the first 4–5 meaningful words, lowercase, hyphenated.
    For example: `add dark mode support for the dashboard` → slug `add-dark-mode-support`.
  - **Validate the generated slug** against `^[A-Za-z0-9._-]+$`, additionally rejecting `.`, `..`, or any value containing `..` (the regex alone permits a dot-only value, which can traverse a directory when a scoped identifier is used as a standalone path segment, e.g. `attachments/<scope>/`), before it is used in any path — it is load-bearing for every scoped temp file constructed downstream (context bundle, plan filename, explore notes, diff/review temp files, PR body, followup ticket temp files, and Phase 9's cleanup list). On failure, clear the Goal Autopilot condition (`/goal clear` — a safe no-op here, since ticketless mode never arms a goal), report the invalid slug value and that it failed validation, and hard-stop: do not sanitize, re-derive, or continue. `ticketId` (ticket mode) is out of scope for this check — it is already constrained to `^\d+$` above.
  - There is no ticket ID and no separate user context — the task description is the primary input.

The determined mode (ticket or ticketless) governs conditional behavior throughout the rest of this skill.

A numeric ticket ID is now known whenever this is ticket mode — either from the bare ticket-ID first token, or extracted from a `.plans/<id>-*.md` filename argument above. **Plan Verification** (see `## Pre-flight Check` below) validates and resumes any saved plan for it via `cenci pipeline plan-check <id>` — that call itself invokes `gh`, so it runs after Auth Verification confirms `gh` is authenticated, not here.

**If ticket mode:** Do **not** fetch the ticket in the main agent (the only exception is `cenci pipeline plan-check`, covered by Auth Verification below and run immediately after it). Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`) for later commands; the ticket itself is fetched by the context-gatherer (see Context Gathering below) after the pre-flight check.

**If ticketless mode:** No ticket to fetch. The task description from `$ARGUMENTS` is the primary input.

## Pre-flight Check

### Auth Verification

**Before delegating to the context-gatherer (or before proceeding in ticketless mode)**, extract `owner/repo` from `git remote get-url origin` (for later commands) and verify `gh` authentication. cenci runs inside the cenci-sandbox container with `--dangerously-skip-permissions`, so Claude Code ignores `permissions.allow/deny` — there is nothing to verify or auto-fix there. What still matters is authentication: the context-gatherer runs read-only `gh` commands in a subagent, and an unauthenticated `gh` deadlocks that subagent silently.

- Run `gh auth status`.
- If it returns authenticated → proceed to context gathering.
- If **not** authenticated → tell the user to run `gh auth login`, then stop. Do not delegate to the context-gatherer until `gh` is authenticated.

### Plan Verification

**Ticket mode only**, immediately after Auth Verification passes (this call invokes `gh`, so it must not run before authentication is confirmed): invoke `cenci pipeline plan-check <id>`, passing `--replan-requested` when the user context contains a standalone token like `replan`/`re-plan` or an explicit instruction to discard/redo the existing plan (this is the **re-plan over an existing plan** path referenced in the Label "Working" section). The CLI owns discovery (globbing `.plans/<id>-*.md`), validation (front matter, required sections, slug), and the freshness/resume decision — do not glob `.plans/` or hand-parse front matter yourself; consume the returned `plan` metadata (`mode`, `slug`, `ticketId`, `isChild`, `isLastChild`, `parentId`) instead. Store the returned `decision` as `planCheckDecision` (Phase 1 reads it) and render its verdict:

- **`resume`** → switch to plan file mode: set `hasPlanFile = true`, `Read` the plan file at the returned `artifacts[0]` path for its full content (the CLI validates and echoes metadata only, not file content) — source ticket details, user context, Q&A, implementation plan, architectural context, design context, and attachment summaries from it — and tell the user in one line: "Found saved plan `.plans/<filename>` — resuming implementation from it (pass `replan` as context to discard it instead)." Do **not** ask — a plan file only exists because a Phase 1 planning session persisted it after the user answered its clarifying questions, launching this run is the human authorization to execute it, and scripted launches (`cenci run implement <id>`) rely on this being non-interactive.
- **`stale`** → same as `resume` above (set `hasPlanFile = true`, read the plan file) but do **not** print the resuming notice — Phase 1's `## Existing Plan` renders the stored `planCheckDecision` and asks the human before continuing (see `phases/phase-1-plan.md`).
- **`replan`** → ignore the plan file (leave `hasPlanFile` unset) and proceed in normal ticket mode.
- **`none`** → proceed in normal ticket mode. This is the everyday outcome for a first `/cenci:implement <ticket-id>` run on a ticket with no saved plan yet — not an error. Do not surface its `errors[]` entry to the user as a failure.
- **`multiple`** → ambiguous; ask with `AskUserQuestion` which plan file to use, offering each path from the returned `artifacts[]` plus **"Re-plan from scratch"** as the final option. Picking "Re-plan from scratch" proceeds in normal ticket mode. Picking a specific path: `cenci pipeline plan-check` has no way to target one candidate among several for validation, so `Read` that file directly and parse its YAML front matter for `mode`/`slug`/`ticketId`/`isChild`/`isLastChild`/`parentId` — the one narrow exception to "the CLI owns discovery/validation" above, mirroring the ticketless-mode fallback earlier in this section, and justified the same way: a structurally CLI-unresolvable disambiguation, not the common case. Set `hasPlanFile = true` and `planCheckDecision = "stale"` — the CLI never computed a freshness verdict for the disambiguated file (it short-circuits to `multiple` before freshness runs), so freshness is *unverified*, not known-fresh; reusing the `stale` branch means Phase 1's existing human-confirmation gate (`## Existing Plan` in `phases/phase-1-plan.md`) runs for this file too, the same safety net every other branch gets. Otherwise proceed like `resume` above.
- **Unrecognized/empty `decision`** (the CLI exited non-zero with `decision` empty and a populated `errors[]` — the plan file exists but is malformed, or its freshness could not be determined at all, e.g. a git or `gh` failure) → do **not** silently fall through to any other branch above. Surface the `errors[]` message to the user and ask via `AskUserQuestion`: "The saved plan couldn't be validated: `<errors[0]>` — re-plan from scratch?" ("Continue with existing plan" is not offered here — unlike `stale`, the plan itself is unreadable/unverifiable, not merely possibly outdated.) If the user agrees, proceed in normal ticket mode (leave `hasPlanFile` unset); otherwise stop.

**If this run just set `hasPlanFile = true`** (the `resume`, `stale`, or `multiple`-disambiguated-to-`resume` branches above): before proceeding, invoke `cenci pipeline prepare <id>`. Context Gathering below is skipped entirely in plan-file mode — including its own `cenci pipeline prepare <id>` call — so this is the call that ensures the ticket's persisted pipeline stage is at least `prepared` before Ticket Ownership's `label --transition working` runs later in this run. It is a monotonic no-op once the ticket is already at or past `prepared` (the common case for a `resume`/`stale` pickup): it returns the persisted stage unchanged with a `warnings[]` no-op entry, no `gh` re-verification performed. Only when the persisted stage is genuinely `new` (e.g. `.cenci/pipeline/` was deleted, or the plan file predates the pipeline CLI) does this call perform a real transition, which also runs the retried `gh issue view <id>` existence re-verification (safe here: Auth Verification confirmed `gh` authentication immediately above). If it returns non-empty `errors[]`, surface them to the user and stop.

**If ticketless mode:** Skip Plan Verification entirely — the pipeline commands operate on ticket IDs.

## Context Gathering (Delegated)

Runs **after** the Pre-flight Check above — the `gh auth status` check is the precondition that makes read-only `gh` safe inside a subagent (see the `subagent-safety` skill).

> **Blocking delegation — do not background or poll.** Invoke the `context-gatherer` as a single, foreground `Task` call and wait for its result inline. Do **not** run it in the background, do **not** announce that you'll "wait for it to complete," and do **not** call any monitoring/polling tool to check on it — the `Task` call returns the digest directly when the subagent finishes. The next pipeline step reads that returned digest; there is nothing to poll.

**If plan file mode** (`hasPlanFile = true`): skip this delegation entirely — the plan file already contains the bundled context, and `isChild`/`isLastChild`/`parentId` come from the `plan` metadata `cenci pipeline plan-check` returned during Pre-flight Check's **Plan Verification** above. Ticket-drift freshness is checked by that same CLI call, not by a main-agent `gh` re-fetch.

**If ticket mode:** Delegate to the `context-gatherer` agent. Pass:

- The ticket number and `owner/repo`
- The bundle output path: `/tmp/claude/cenci-context-<ticket-id>.md`
- Config facts: `isMonorepo`, `projects`, and the design path when enabled

The gatherer fetches the ticket and comments, performs parent-child detection, discovers attachments, loads design and per-project context, writes the bundle file, and returns a compact digest. From the digest, store:

- `isChild`, `isLastChild`, `parentId` — for commit, PR body, and labeling
- `labels` — for the Ticket Readiness checks
- The attachment list — for the Attachments step
- `bundlePath` — passed to the planner and appended to the plan file in Phase 1

If the digest reports errors (ticket not found, auth failure), surface them to the user and stop. Do **not** re-fetch the ticket or re-read DESIGN.md in the main agent — the digest and bundle are the source of truth.

After the digest is stored, invoke `cenci pipeline prepare <id>` to record `prepared` state and confirm the ticket exists (the command itself re-verifies via a retried `gh issue view <id>`). Render the returned `next_actions` and `warnings` as the pre-flight status update instead of narrating what comes next — a `warnings` entry here means this `prepare` call was a monotonic no-op (the ticket's persisted stage was already at or past `prepared`), which is worth showing in the transcript like every other stage call site. If it returns non-empty `errors[]`, surface them to the user and stop — do not proceed to Attachments/Ticket Readiness. Ticketless mode: skip this call — the pipeline commands operate on ticket IDs.

**If ticketless mode:** Delegate to the `context-gatherer` only when there is context to bundle (design enabled or monorepo), with the task description in place of the ticket and bundle path `/tmp/claude/cenci-context-<slug>.md`. Otherwise skip — the task description is the entire input.

**Parent-child edge cases** (resolved inside the gatherer, recorded here for downstream phases):
- Parent already closed → `isLastChild = false` (skip auto-close)
- Parent/child links resolve from the native sub-issue graph (`--json parent` for the parentId, `--json subIssues` for siblings); when a ticket predates native linking and has no sub-issue nodes, the gatherer falls back to the `Related to #<parentId>` search
- Some siblings manually closed → they don't count as open, don't block last-child detection

## Attachments

Runs after Context Gathering (which itself runs after the Pre-flight Check). The effective order is: mode detection → Pre-flight Check → Context Gathering → Attachments → Ticket Readiness.

**If ticketless mode:** Skip the Attachments section entirely.

**If plan file mode:** Skip — attachment summaries are already in the plan file.

**If ticket mode:** The context-gatherer digest already contains the discovered attachment list (Step 1 of the procedure). Read the `attachments` reference skill and follow Steps 2–4 (present, download, load) using that list. If the digest reports no attachments or the user selects none, proceed.

Store each attachment's file path for passing to subagents (subagents share the filesystem and can read attachments directly via `Read`).

## Ticket Readiness

**If ticketless mode:** Skip the Ticket Readiness check entirely and proceed to the Pipeline. Still set `isUiTicket = true` when the task description matches the frontend classification in the Design Check below (there are no labels to gate on, so the gate itself is skipped, but Phases 4 and 9 use the flag for screenshots).

**If plan file mode:** Skip this check — readiness was verified when the plan was created. Set `isUiTicket` from the plan's ticket title/requirements using the same frontend classification.

**If ticket mode:** After context gathering, inspect the ticket's labels/tags before starting the pipeline:

Check the `labels` line from the context-gatherer digest.

### Design-Ticket Router (first check)

If the labels include **"Design"**, this is a design-only ticket — its deliverable is a design spec (`.pen` + `DESIGN.md`) produced by `/cenci:design`, not code. Ask via `AskUserQuestion`:

> "This ticket is labeled `Design` — its deliverable is a design spec, not a code change. It should be run through `/cenci:design <ticket-id>` instead. How do you want to proceed?"

- **"Stop — route to /cenci:design (Recommended)"** — stop the pipeline immediately. Tell the user to run `/cenci:design <ticket-id>`. No labels change (the "Working" label has not been applied yet at this point).
- **"Proceed with implementation anyway"** — the label may be stale or wrong. Continue with the remaining readiness checks below, and tell the user to remove the `Design` label (or re-run `/cenci:refine`) if the ticket genuinely includes implementation work.

Do not proceed past this check without an explicit answer. (Plan-file mode skips Ticket Readiness entirely, which is fine — a design-only ticket never produces a plan file.)

### Remaining Readiness Checks

If the ticket does **not** have a "Refined" label/tag, display a warning:
> "This ticket hasn't been refined yet. Consider running `/cenci:refine <ticket-id>` first for better results. Do you want to proceed anyway?"

If the user says no → stop. If yes → proceed with the pipeline.

If the digest's `labels` already include **"In Review"** or **"Implemented"**, a PR already exists for this ticket (open or merged). Display a soft, non-blocking warning via `AskUserQuestion` — mirror the "not refined" tone:
> "This ticket already has an open PR (In Review)." — or, for "Implemented" — "This ticket's PR has already merged (Implemented). Re-implementing will open a second PR for the same ticket. Do you want to proceed anyway?"

If the user says no → stop. If yes → proceed with the pipeline. This is a warning only — it does not block.

If the digest's `labels` include **"Planned"** but this run is **not** plan-file mode (`hasPlanFile` is false — e.g. the plan file was deleted or lives on another host), a plan was recorded for this ticket but no plan-file argument reached this run. Display a soft, non-blocking note via `AskUserQuestion` — mirror the tone above:
> "This ticket is marked `Planned` (a plan was persisted), but you didn't pass a plan file. If the plan file still exists under `.plans/`, re-run as `/cenci:implement <ticket-id>` or `/cenci:implement .plans/<file>` to pick it up (`cenci pipeline plan-check` resumes from it automatically when a matching file is found); otherwise you can re-plan from scratch. Proceed with a fresh plan anyway?"

If the user says no → stop. If yes → proceed (a fresh plan re-applies `Planned` at the end). This is a warning only — it does not block.

### Design Check (hard gate)

Read the `frontend-classification` reference skill and apply its rule to the ticket title and digest summary. If the ticket is classified as frontend, set `isUiTicket = true`. Phase 4 and Phase 9 use this flag for screenshot capture and PR embedding.

If `isUiTicket` is true and the ticket does **not** have a "Designed" label/tag, **stop and ask** before starting the pipeline. UI tickets implemented without an approved design are the most error-prone, even with a design system in place.

A bundled `DESIGN.md` does **not** satisfy this gate on its own: the design path persists across tickets, so an existing `DESIGN.md` may describe a previous ticket's design. Only the "Designed" label on this ticket counts.

Ask via `AskUserQuestion`:

> "This UI ticket has no "Designed" label. [If the digest reports a bundled `DESIGN.md`, add: A `DESIGN.md` was found at `<path>`, but it may belong to an earlier ticket.] How do you want to proceed?"

- **"Stop — design first (Recommended)"** — stop the pipeline. Design lives on a dedicated design ticket, so tell the user: if this ticket already depends on a `Design`-labeled ticket (look for a `Depends on #<n>` line in the ticket body / digest summary), run `/cenci:design <design-ticket-id>` — completing it closes the design ticket and propagates `Designed` to this one. If no design ticket exists, re-run `/cenci:refine <ticket-id>` to create the companion design ticket. Re-run `/cenci:implement` once this ticket carries the "Designed" label.
- **"Proceed without design"** — continue the pipeline. Record the choice; Phase 9 notes "implemented without design spec" in the PR body.

Do not proceed past this gate without an explicit answer.

### Visual Check Reminder

If the ticket has a `ui:visual-check` or `Browser` label, display a reminder:
> "This ticket has the `ui:visual-check` label. Ensure `playwright-cli` is available for visual verification (`playwright-cli screenshot`, `playwright-cli snapshot`)."

This is informational only — it does not block the pipeline.

## Ticket Ownership

**If ticketless mode:** skip this section.

**If ticket mode:** before triage, invoke `cenci pipeline label <id> --transition working` — the CLI now owns both the ownership verify/auto-claim logic (mirroring the `ticket-ownership` reference skill's own logic: verify exclusive ownership, auto-claim an unassigned ticket, never replace an existing assignee) **and** applying the `Working` label in one call (see the **Label "Working"** section below, which this same call also satisfies). Render the returned `state`/`next_actions`/`warnings`/`errors` as this step's status update; if it returns non-empty `errors[]` (foreign/multiple assignee, wrong pipeline stage), surface them and stop before proceeding to triage or the pipeline.

The `ticket-ownership` reference skill itself stays in place — it is still read directly by `/refine` and `/design`, which don't run through the pipeline CLI. This call site no longer reads it directly; the CLI reimplements the same logic instead.

## Trivial-Ticket Triage

**Ticket mode only.** Skip this section entirely in ticketless mode and in plan file mode — plan file mode already has a persisted plan, and ticketless mode has no ticket body to triage against.

Runs after every Ticket Readiness gate above (Design-Ticket Router, "Refined" check, "Designed" hard gate) — a ticket disqualified by those gates never reaches triage.

This is a cheap heuristic the main agent evaluates directly over the context-gatherer digest already fetched, plus (see below) a single direct read of the already-gathered bundle file — no subagent invocation, no codebase exploration, no additional tool calls beyond that one `Read`.

The digest alone is not sufficient for the last two gates below: it is capped at ~40 lines and is explicitly a 3-6 bullet paraphrase of the ticket, never the verbatim body (see `agents/context-gatherer.md`'s Digest format). Judging "no ambiguity" or "triage can name the specific file(s) directly" against a paraphrase is only as reliable as that paraphrase's fidelity. The bundle file at the digest's `bundlePath:` was already written to disk this session at zero extra cost, so triage may (and for these two gates, should) `Read` its `## Ticket Details` section for the exact verbatim ticket body wording, in addition to the digest. This is still "no subagent invocation, no codebase exploration" — reading a file already gathered this session is neither. If that `Read` fails for any reason, treat it the same as failing to clearly qualify — fall through to the normal planning flow rather than judging trivial off the digest alone.

A ticket qualifies as trivial only when **all** of the following hold:

- Not security-sensitive, not a data migration, not auth/payment-related.
- Not UI work (`isUiTicket` is false).
- Fully specified by the ticket body — no ambiguity that would otherwise require a clarifying question.
- Bounded to an obvious, narrow change — triage can name the specific file(s)/change directly from the ticket body alone.

Conservative default: if the ticket does not clearly qualify on all four points, fall through to today's unchanged planning flow (planner delegation, Q&A, persist-and-stop). Ambiguity always falls through — never guess trivial.

### Sensitive-path backstop (deterministic)

The first criterion above ("not security-sensitive, not a data migration, not
auth/payment-related") is a wording-based judgment over the ticket text. It stays as-is
and is preserved as the first pass — but a ticket can be security-relevant without saying
so. So once all four criteria pass, run one additional, deterministic check over the file
path(s) triage already named from the ticket body under criterion 4. Those paths are
already in hand; obtaining them requires no new work. If criterion 4 was instead satisfied
via a change description with no identifiable file path, the backstop has nothing to
pattern-match against — treat this as inconclusive and fall through to full planning (never
trivial), consistent with this section's conservative-default philosophy.

**Pattern set — built-in defaults unioned with project config.** Match each named path
against the union of two sources:

1. The built-in default sensitive-path patterns below. These **always apply**, even for a
   project that has configured nothing:

   - `*auth*` (authentication, authorization, oauth, authService, …)
   - `*login*`, `*logout*`, `*session*`
   - `*password*`, `*passwd*`, `*credential*`, `*secret*`, `*secrets*`
   - `*token*`, `*jwt*`, `*apikey*`, `*api_key*`, `*.pem`, `*.key`, `*.env*`
   - `*oauth*`, `*sso*`, `*saml*`, `*openid*`
   - `*permission*`, `*acl*`, `*rbac*`, `*role*`
   - `*crypto*`, `*encrypt*`, `*decrypt*`, `*sign*`, `*hash*`
   - `*payment*`, `*billing*`, `*invoice*`, `*checkout*`, `*stripe*`
   - `*migrat*` (migration / migrations / migrate), `*schema*`

   This list is intentionally broad. A false positive costs only the fast path — the ticket
   still gets fully planned, never wrongly judged trivial — so err toward matching.

2. Any glob strings in `security.sensitivePaths` from the resolved config (already read in
   the Context section — this adds no tool call). Project entries are **additive**: they
   extend the built-in defaults and never replace or narrow them. A project that omits
   `security.sensitivePaths`, or omits the `security` block entirely, still gets the full
   default list.

**Match semantics — whole-path substring.** A pattern matches when, treating every `*` as
matching any run of characters **including `/`**, the glob matches the entire
repository-relative path. In practice `*<term>*` means "the path contains `<term>` anywhere
— in any directory segment or in the filename, freely across `/` boundaries." Matching is
case-insensitive. For example, `*auth*` matches `src/auth/login.ts`, `authService.ts`, and
`lib/oauth.ts` alike.

**Outcome.** If any named path matches any pattern in the combined set, force
`trivial = false` and fall through to the normal planning flow — the same outcome as failing
any of the four criteria — regardless of what the wording-based first pass concluded. This
backstop can only **disqualify** a ticket from the fast path; it never promotes one.

**Zero new tool calls.** The match runs in-model over the path strings already named under
criterion 4 (and the bundle body already read for the two fidelity gates above). Do **not**
`Glob`, `Read`, or invoke a subagent to resolve, expand, or verify paths — this stays inside
the section's "no subagent invocation, no codebase exploration" constraint.

**Conservative fall-through on failure.** If the resolved config carries no `security`
block, apply the defaults alone. If `security.sensitivePaths` is present but malformed or
unreadable (not an array of strings), ignore only the configured entries and still apply the
built-in defaults — never skip the backstop entirely. When this malformed-config fallback is
taken, surface it in the chat-level completion summary the user actually reads (not only as
an inline comment) — e.g. print a one-line notice such as "Ignoring malformed
`security.sensitivePaths` — applying built-in defaults only" — so a project's custom
sensitive-path coverage silently breaking doesn't go unnoticed. And consistent with the
conservative default above, if there is any doubt about whether a named path matches, treat
it as a match and fall through to full planning rather than judging trivial.

When it qualifies, set a session flag `trivial = true` plus a short `reason` string, and print one line (no `AskUserQuestion`, no confirmation):

```text
Judged trivial: `<reason>` — skipping planning, implementing directly
```

Phase 1 reads `trivial` and, when true, takes the **Trivial Fast Path** (see `phases/phase-1-plan.md`) instead of the `## New Plan` planner delegation.

## Label "Working"

**If ticketless mode:** Skip this section entirely — ticketless mode applies no board labels.

**If ticket mode:** Before starting the pipeline, the `cenci pipeline label <id> --transition working` call from the **Ticket Ownership** step above is what applies the "Working" label to signal work in progress — there is no separate label-application step here; this section explains *why* that label is applied, not a second mechanism for applying it. The CLI self-heals the label's existence (creating it in the repository on first use, same as it always did) and treats "already exists" as success, so no separate self-heal call is needed here either.

`Planned` is a milestone marker, not a current-stage indicator — once a ticket has a persisted plan, it keeps that label for the life of the ticket (only `In Review`/`Implemented` reaching the ticket via the Phase 9 flow, or the ticket closing, ever implies otherwise). Only `Working` toggles on and off as the pipeline runs. So the implement skill never removes `Planned` — this holds even when starting from a plan-file pickup or discarding a plan to re-plan from scratch.

The reasoning behind the single `--transition working` call is the same regardless of how this run entered the pipeline:

- **Plan-file mode** (`hasPlanFile` true): this is the saved-plan pickup. The ticket already carries `Planned` from when the plan was persisted; the call above just adds `Working` alongside it.
- **New-plan ticket mode** (`hasPlanFile` false): the call above adds `Working`. This also covers a session entered via a **re-plan over an existing plan** (the user context requested `replan`, or "Re-plan from scratch" was chosen in the multiple-match question) — the ticket still carries `Planned` from the discarded plan, and that's fine: it stays. In a new-plan session `Working` is short-lived: Phase 1 swaps it back out (`cenci pipeline label <id> --transition planned`) when it persists the fresh plan and stops. **Exception — Trivial Fast Path**: when Trivial-Ticket Triage set `trivial = true`, Phase 1 retains `Working` instead of swapping it out (`cenci pipeline label <id> --transition planned --trivial`) — it adds `Planned` alongside the already-present `Working`, because the session continues into Phase 2 rather than stopping.

## Pipeline

This pipeline has 9 phases, grouped into 5 coarse stages tracked by `cenci pipeline <stage> <id>`: **prepare** (pre-flight/context, before Phase 1), **plan** (Phase 1), **execute** (Phases 2–5), **review** (Phases 6–7), **finalize** (Phases 8–9). Execute the phases in order without stopping for confirmation — the user pre-approved all phases, including commit, push, and PR creation, by invoking this skill. At each coarse-stage boundary, invoke `cenci pipeline <stage> <id>` and render the returned `state`/`next_actions`/`warnings`/`errors` as the one-line status update between major phases, instead of prose-deriving "what's next" — then immediately continue per those `next_actions`; do not wait for acknowledgment. **Read each phase file only when you reach that phase** — do not read all files upfront.

The only reasons to stop mid-pipeline are explicit error gates defined within individual phases (rebase conflicts, repeated build failures, push auth errors, unclear reviewer findings) or a `cenci pipeline <stage> <id>` call returning a non-empty `errors[]` — this applies uniformly to every stage, including `review` and `finalize`: pipeline state is now anchored to the main-checkout root and shared across the main-checkout → feature-worktree boundary, so a non-empty `errors[]` from either call is an authoritative hard-stop like any other stage — clear the Goal Autopilot (`/goal clear` via `SlashCommand`, a no-op if none is armed) before stopping and reporting (see `phase-6-7-review.md`/`phase-8-docs.md` for the exact stop procedure at each of those two stages). If no error gate fires, complete all 9 phases. The pipeline is not complete until a PR URL has been created and returned to the user — never end with a status summary like "ready for PR" or "branch is ready." The terminal state is a PR that is **open and handed off to the `cenci babysit` supervisor** (Phase 9's final step), which then carries it through CI, review feedback, and the final `In Review` → `Implemented` relabel on merge — implement itself does not carry the PR to merged, so it reports "PR open and being watched," not "done." That babysit hand-off is a post-goal, best-effort step: the Goal Autopilot condition below still clears at PR creation and never waits on the babysit launch (see the Goal Autopilot clearing rules), so a failed watcher launch never re-loops the pipeline.

**Hard stop after planning**: Phases 2–9 run only when the skill was invoked with a plan-file argument (`hasPlanFile` set during mode detection). A session that creates a new plan **always ends at Phase 1** — after persisting the plan file, do not read `phases/phase-2-worktree.md` or any later phase file. Implementation resumes via `/cenci:implement .plans/<filename>` in a fresh session. The sole exception is the **Trivial Fast Path**: when Trivial-Ticket Triage judged the ticket trivial, Phase 1 still persists a plan file, but the session does not stop — it continues straight into Phase 2 in the same session (see `phases/phase-1-plan.md`'s `## Trivial Fast Path` and `phases/phase-2-worktree.md`'s `## Gate Check`).

### Goal Autopilot (plan-file mode)

Phases 2–9 run unattended, but a turn that stops mid-phase (context limit, transient tool error, a subagent that ends the turn) just ends the run — the work is left half-done with no PR. Claude Code's native `/goal` closes that gap: it registers a session-scoped completion condition and, whenever a turn ends without the condition met, immediately starts another turn. Armed at pipeline start, it turns "stopped mid-phase" into "resumes mid-phase."

**Launching the plan-file run is the human gate that arms it.** The goal is never set in an ordinary session that *creates* a plan — that session ends at Phase 1 after persisting the plan and presenting it for review, and goals are session-scoped (a new session or `/clear` drops them). The human reviewing the saved plan and launching `/cenci:implement .plans/<filename>` is what authorizes the autonomous run; the goal is armed when that run begins — i.e. **only in plan-file mode (`hasPlanFile = true`), at the start of Phase 2** (see `phases/phase-2-worktree.md`). Ticketless and ordinary ticket-mode planning sessions never arm a goal. (The Trivial Fast Path is the one ticket-mode planning session that both creates a plan and arms a goal — see the next paragraph.)

**Trivial Fast Path also arms at Phase 2 start.** Arming keys off `hasPlanFile = true` at the start of Phase 2, and the Trivial Fast Path reaches Phase 2 with `hasPlanFile = true` in the same session (see `phases/phase-1-plan.md`'s `## Trivial Fast Path` and `phases/phase-2-worktree.md`'s `## Gate Check`) — so it arms the goal exactly like a plan-file-argument run does: same availability gate, same condition semantics, same clearing rules. Nothing else changes. The conservative triage heuristic in `## Trivial-Ticket Triage` above (with its conservative fall-through) substitutes for the human plan-review gate in this one case.

**Availability gate (do this once).** `/goal` needs no separate version or binary check — older Claude Code clients below 2.1.139 lack `/goal` support, but the pipeline never checks that directly; step 2 below is both the check and the arming attempt. When entering Phase 2 in plan-file mode (or via the Trivial Fast Path reaching Phase 2 with `hasPlanFile = true`):

1. If the resolved config has `cenci.goalAutopilot: false`, skip the goal entirely.
2. Otherwise attempt to arm `/goal` directly via the `SlashCommand` tool, treating a missing tool, unknown command, or error as Goal Autopilot being unavailable. On that outcome, **skip silently** and proceed exactly as today — print one line naming the specific cause that matched: `Goal autopilot unavailable (<cause>) — running without a completion guarantee.` where `<cause>` is one of:
   - `SlashCommand tool not available in this session` — the client itself lacks the `SlashCommand` tool; remedy is upgrading to a client version that supports it.
   - `/goal is not a known command in this session` — the client has `SlashCommand` but no `/goal` command registered; remedy is adding `SlashCommand(/goal:*)` to `.claude/settings.json`.
   - `/goal invocation returned an error` — the command exists but the arming call itself failed; remedy is reporting the error text the invocation returned.

   The pipeline's behavior with no goal is unchanged from prior versions.

**Arming.** The attempt in step 2 above IS the arming call — there is no separate probe-then-arm sequence. Invoke the `/goal` slash command (via the `SlashCommand` tool) with a condition that references the persisted plan file so it stays consistent with the `check-pending-plans` SessionStart hook (which treats a still-present `.plans/<filename>` as "resume this"):

```
/goal Implementation of the plan file .plans/<filename> is complete: every remaining cenci implement phase (2–9) has run and a pull request has been created, with its URL printed in this session. The plan file .plans/<filename> is deleted only in Phase 9 after the PR exists — while it is still on disk, the work is NOT done. Do not treat the goal as met on a build failure, lint failure, rebase conflict, failed push, or any state that was handed back to the user for a decision. Safety cap: if this goal has restarted the turn more than 20 times without the pipeline advancing to a new phase, stop retrying — run /goal clear and report the stall to the user instead of continuing.
```

If the `SlashCommand` tool is not available or the command errors, that is the unavailable outcome from the gate above — proceed without it, never let goal setup block the pipeline. Configs generated by `/cenci:configure` allow `SlashCommand(/goal:*)`, so arming does not prompt; on an older config that lacks it the invocation may prompt once — allow it (or add `SlashCommand(/goal:*)` to `.claude/settings.json`) to keep later runs unattended.

**Known limitation — no mechanical turn counter.** `/goal` does not expose a counter the pipeline can read or increment; the 20-turn cap above is model-tracked by reasoning over the conversation transcript (how many times this goal has already restarted the turn while stuck on the same phase), and that tracking is best-effort after a context compaction event erases earlier turns from view. This is a deliberate choice, not a gap to fill later — no counter-file or other bookkeeping machinery is being built for this; the cap is a coarse safety valve against runaway retries, not a precise guarantee.

**Clearing — three paths, all mandatory:**

- **Success**: Phase 9 runs `/goal clear` immediately after the PR is created (see `phases/phase-9-pr.md`), before the plan-file cleanup.
- **Any human-input stop**: because `/goal` restarts the turn on every stop, an *un-cleared* goal turns a genuine blocker into an infinite retry loop. So **before any mandatory stop that hands control back to the user** — the error gates in Phases 2–9 (worktree check failure, a red baseline gate (Phase 2), unclear test requirements, build/test failures after retries, an ambiguous reviewer finding needing a human decision, a rebase conflict, a push auth/network failure) — run `/goal clear` first, then stop and report as the phase instructs. Clearing is a no-op when no goal was armed, so it is always safe to call.
- **Turn-cap stall**: if the same goal has restarted the turn more than 20 times without the pipeline advancing to a new phase (the safety cap baked into the condition string above), run `/goal clear` and stop, reporting which phase it stalled on and the best available diagnosis — a runaway retry loop is itself a state that needs the human back, exactly like the other error gates.

`/goal` = keep going until the PR exists; launching the plan-file run = the human gate that arms it; the error gates and the turn-cap stall = the points where the human must re-enter, so the goal stands down there.

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

Read the resolved config for optional `cenci` settings:

- `cenci.compactImplementation: true` — for small, low-risk plans only, Phase 3, 4, and 5 may be handled by a single implementer delegation. The implementer must still explicitly report red test failures, green implementation, refactoring, and final build/test results. Do not use this mode for security-sensitive, data migration, auth/payment, large UI, or unclear-requirement work.
- `cenci.reviewConcurrency: "sequential"` — run the same Phase 6 + 7 reviewers one after another instead of in parallel. Quality gates are unchanged; this only smooths usage limits. Default is `"parallel"`.
- `cenci.implementerConcurrency: "sequential"` — when the plan declares `### Parallel Lanes` (see Phase 3's `## Parallel Lanes`), run the lane implementers one after another instead of concurrently. Quality gates are unchanged either way — same per-lane red-before-green discipline, same Lane Verification Barrier; parallel trades higher token usage for lower wall-clock time, and `"sequential"` smooths usage limits. Default is `"parallel"`. This setting never *creates* lanes — a plan without a lanes section (or one failing Phase 3's eligibility re-check) always runs the standard sequential flow.
- `cenci.liteReviewEnabled: false` — opt out of the lite review path (see Phase 6 + 7). Default (unset or `true`): Phase 6 + 7 classifies each diff into one of three paths — `full` (all three reviewers, today's behavior) for source changes, oversized diffs, or anything touching `.claude/**`, `skills/**`, `agents/**`, `CLAUDE.md`, or a danger pattern (auth/security/secrets/CI workflows); `lite-docs` (no reviewers) for docs-only diffs; `lite-small` (`code-reviewer` only, same model, same Code Review Actions) for small config/data-only diffs. Setting this `false` forces the full trio on every run, regardless of diff size or content.
- `cenci.diffContextMode: "file"` — before Phase 6 + 7, write the full diff to `$RUN_DIR/diff.patch` (this run's artifact directory, created once via `scripts/run-artifact-dir.sh` — see Phase 6 + 7) and pass reviewers the path plus changed file list and stat. Reviewers read only the hunks they need. Default is `"inline"` for small diffs.
- `cenci.goalAutopilot: false` — opt out of the Goal Autopilot (see the Pipeline section). Default (unset or `true`) attempts to arm a `/goal` completion condition at Phase 2 start, falling back to running without one if `/goal` is unavailable, so a mid-phase stop resumes instead of ending the run. This is not a cost control — it trades a small evaluation overhead per turn for the completion guarantee; set it `false` to run phases 2–9 without one.
- `cenci.planComment: true` — not a cost control. When `true`, Phase 1 also posts the saved plan as a ticket comment (ticket mode only) for audit / off-host visibility before marking the ticket `Planned` (the label call must stay the last ticket edit — it records the plan-freshness baseline); `.plans/` stays the executable source of truth. Default (unset or `false`): no comment. Canonical schema lives in `/cenci:configure`.

If a cost-control setting conflicts with quality, ignore the setting and explain why.

Proceed to Phase 1 by reading `phases/phase-1-plan.md`.
