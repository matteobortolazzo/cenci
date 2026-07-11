---
name: refine
description: "Claude Code-only: refine a ticket interactively until it is ready for planning."
compatibility: Requires Claude Code AskUserQuestion and agentflow project configuration.
argument-hint: <ticket-id> [additional context]
user-invocable: true
disable-model-invocation: true
model: opus
allowed-tools: Read, Glob, Bash(gh:*), Bash(git:*), Bash(curl:*), Bash(mkdir:*), AskUserQuestion, WebFetch
---

## Context

**Config check**: Before anything else, verify `.claude/config.json` exists by reading it. If the file does not exist, **stop immediately** and tell the user:
"agentflow is not configured for this project. Run `/agentflow:configure` first to set up."

Read `.claude/config.json`.

**Parse `$ARGUMENTS`:**
The first token is the ticket ID. Everything after it is optional **user context** (additional instructions or focus areas).

Split `$ARGUMENTS` into:
- **Ticket ID**: the first whitespace-delimited token, with any leading `#` prefix stripped.
  For example: `#1 focus on API` → ID `1`, `7` → ID `7`.
- **User context**: everything after the first token (may be empty).
  For example: `42 focus on the API layer` → context is `focus on the API layer`.

**Shell rules**: Read the `shell-rules` skill before running any `gh` commands (covers heredoc temp-file pattern).

**Fetch the ticket:**
Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`), then run:
```bash
gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,comments
```

## Attachments

Read the `attachments` reference skill and follow its 4-step procedure to discover, present, download, and load ticket attachments. If no attachments are found or the user selects none, proceed to Pre-flight Checks.

Store each image summary alongside its file reference for use during refinement.

## Pre-flight Checks

After fetching the ticket, inspect its current state before proceeding:

Check the issue's `labels` array and `state` field.

If any of these conditions are true, warn the user and ask for confirmation using `AskUserQuestion`:

| Condition | Message |
|-----------|---------|
| Ticket is closed/resolved | "This ticket is closed/resolved. Do you still want to refine it?" |
| Has "Refined" label/tag | "This ticket is already marked as Refined. Do you want to re-refine it?" |
| Has "Planned" label/tag | "This ticket already has an approved plan (Planned). Do you want to re-refine it?" |
| Has "Working" label/tag | "This ticket is currently being worked on. Do you want to re-refine it?" |
| Has "In Review" label/tag | "This ticket has an open PR (In Review). Do you want to re-refine it?" |
| Has "Implemented" label/tag | "This ticket is already marked as Implemented (its PR merged). Do you want to re-refine it?" |

If the user says no → stop. If yes → proceed normally.

## Label "Working"

**Before starting refinement work**, add the "Working" label to signal that the ticket is actively being worked on:
```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Working"
```

## Your Role

You are a senior tech lead doing backlog refinement. Your goal is to ensure this
ticket is unambiguous, well-scoped, and ready for implementation.

## Process

1. **Fetch and summarize** the ticket (title, description, acceptance criteria, linked items)

2. **Read relevant `docs/<topic>.md`** files for the feature area. If a legacy `.claude/rules/lessons-learned.md` exists, read it as fallback.

3. **If user context was provided**, treat it as additional steering input. Focus your analysis and questions on the areas the user highlighted. Mention the user's context when it's relevant to your questions or analysis.

4. **Classify ticket type**: Read the `frontend-classification` reference skill and apply its rule to determine if this ticket involves frontend/UI work. If yes, activate **design-aware refinement** for this session. If purely backend/infrastructure/data, skip design-specific analysis.

   **Design-only classification** (if frontend ticket AND `pencil.enabled` is `true` in `.claude/config.json`): determine whether the ticket's *deliverable* is the design itself — a `.pen` file plus `DESIGN.md` spec, with no production code change (e.g., "Design the settings page", "Create mockups for the onboarding flow"). If the signals point that way, confirm via `AskUserQuestion`:

   > "This reads as a design-only ticket — the deliverable would be a design spec (`.pen` + `DESIGN.md`) produced by `/agentflow:design`, with no code change. Is that right?"

   Options: "Yes — design-only", "No — includes implementation"

   If confirmed, set `isDesignTicket = true`. Design-only tickets are routed to `/agentflow:design`, not `/agentflow:implement`: they skip the browser question (step 8) and the `ui:visual-check` label (step 13), and receive the `Design` label in step 12. Focus the analysis (step 5) on design questions — visual direction, screens, states, design-system fit — and skip implementation-only items (API contracts, database changes, PR size).

   **Design Coverage Check** (if frontend ticket AND `pencil.enabled` is `true` in `.claude/config.json`):

   a. Read `pencil.designPath` from `.claude/config.json`.
   b. Use Glob to check if `.pen` files exist at the configured `designPath` (e.g., `<designPath>/**/*.pen`).
   c. Check if `<designPath>/DESIGN.md` exists. If it does, read it and evaluate coverage:
      - Are screens mapped? (Does the Screens table reference the screens relevant to this ticket?)
      - Are behavior annotations present? (Do mapped screens have interaction/state descriptions?)
      - Are component-to-code mappings documented? (Does the Components table link design components to framework components?)
   d. Report any gaps as informational findings — these are **not blocking**:
      - "Design coverage: N screens mapped, M components mapped, behavior annotations present/missing for [screen names]."
   e. If coverage is insufficient (no `.pen` files found, no DESIGN.md, or significant gaps in mappings), set `designNeeded = true`. **Design always happens on a dedicated design ticket, never on the implementation ticket itself** — the design ticket is created later, either as the first child of a split (see **Design-first splits**) or as a companion ticket (see **Companion design ticket** in the Update Ticket section).

5. **Analyze** what's missing or ambiguous. Consider:
   - Are acceptance criteria specific and testable?
   - Are edge cases covered?
   - Is the scope clear? Could it hide complexity?
   - Are API contracts defined (request/response shapes)?
   - Are there dependencies on other tickets?
   - Is the UI behavior specified (states, loading, errors)?
   - **If frontend ticket** — also evaluate design quality:
     - Is a visual direction or aesthetic tone specified, or will it default to generic?
     - Are typography, color palette, and spatial layout defined with intention?
     - Are motion/animation behaviors described, or will the result be static?
     - Does the ticket risk producing cookie-cutter design (generic fonts, predictable layout, cliched color schemes)?
   - **If frontend ticket AND `pencil.enabled` is `true`** — also evaluate design spec coverage:
     - Are the screens referenced in this ticket present in DESIGN.md?
     - Are behavior annotations present for the affected screens?
     - Are component-to-code mappings documented for all UI components in scope?
     - Are design tokens (spacing, color, typography) referenced for the affected components?
   - Are there security considerations?
   - Is it estimable? If not, what's blocking estimation?
   - If the ticket references existing apps ("like X", "similar to Y"), are the key UX patterns of those references captured? (e.g., layout model, navigation, interaction patterns)
   - Is it small enough for a single PR (~500 lines)? If not, should it be split into separate tickets?

6. **Ask ONE question at a time using `AskUserQuestion`**. Wait for the user's answer before asking the next. Never ask questions as plain text — always use the `AskUserQuestion` tool.
   - Be specific: "What should happen if the user submits an empty form?" not "Are errors handled?"
   - Reference existing code/patterns when relevant: "I see we use toast notifications elsewhere — should errors here also use toasts?"
   - **For frontend tickets — propose design directions instead of asking open-ended questions.** Don't ask "What typography should we use?" — instead propose: "For a [context], I'd suggest [specific font pairing] to avoid the generic Inter/Roboto look. Does this work, or do you have a different direction?" Apply this propose-first pattern to color palette, layout composition, and motion design.
   - Challenge vague design language: "clean and modern" or "professional look" almost always produces generic results. Push for what makes this interface *memorable*.
   - Limit design-specific questions to 2-3 per session. Focus on highest-impact decisions: aesthetic tone, one typography/color choice, and one layout/motion choice.

7. **After each answer**, update your understanding and decide:
   - Ask another question, OR
   - Declare the ticket refined

8. **Before producing the summary**, ask one final infrastructure question — but only when it can plausibly apply. **Skip for design-only tickets** (`isDesignTicket` is true) — they never reach the implement pipeline; set `browserRequired: false`. Ask it if the ticket was classified frontend/UI in step 4, **or** the ticket/answers mention web scraping, browser automation, or manual browser testing. For pure backend/infrastructure/data tickets with none of those signals, skip the question and set `browserRequired: false`.

   Using `AskUserQuestion`:
   "Does this story need interactive browser access during implementation? (e.g., for visual verification, form testing, or web scraping). If yes, the implementer should ensure `playwright-cli` is installed (`npm i -g @playwright/cli`)."
   - If **yes** → note `browserRequired: true` for the labeling step
   - If **no** → proceed normally

9. **When refined**, produce:

   ## Refined Ticket Summary

   ### Updated Description
   <rewritten description incorporating all clarifications>

   ### Acceptance Criteria
   - [ ] <specific, testable criterion>
   ...

   ### Technical Notes
   - Affected services: <list>
   - Affected components: <list>
   - API changes: <list endpoints, methods, DTOs>
   - Database changes: <migrations needed>
   - Dependencies: <other tickets that must complete first>

   ### Design Coverage (if frontend ticket AND pencil.enabled)
   - **Screens mapped**: <list of screen names from DESIGN.md that relate to this ticket>
   - **Missing annotations**: <any screens lacking behavior annotations>
   - **Unmapped components**: <UI components without code mappings in DESIGN.md>
   - **Design tokens**: <coverage status — defined/missing for affected components>

   ### Design Direction (if frontend ticket)
   - **Aesthetic tone**: <chosen direction, e.g., "editorial with high-contrast typography">
   - **Typography**: <font pairing with rationale>
   - **Color palette**: <dominant + accent, hex values>
   - **Key motion**: <entrance animations, hover states, transitions>
   - **Layout approach**: <spatial strategy, any grid-breaking elements>
   - **Anti-patterns to avoid**: <generic choices explicitly ruled out>

   ### Size Estimate
   <S/M/L> — <reasoning>

   ### Suggested Split (if M or L)
   - Ticket 1 (1/N): <description>
   - Ticket 2 (2/N): <description> — depends on Ticket 1
   - Ticket 3 (3/N): <description> — parallel with Ticket 2
   (Each becomes its own numbered ticket and PR. The parent ticket tracks all children and their dependencies.)

   #### Execution Order
   - Ticket 1 → first (no dependencies)
   - Ticket 2, Ticket 3 → can start after Ticket 1 (parallel with each other)

   When analyzing the split, determine which child tickets have data/API/schema dependencies on others (sequential) vs. which touch independent areas (parallel). Annotate each ticket accordingly.

   **Design-first splits** (if frontend feature AND `pencil.enabled` is `true` AND `designNeeded` is true): make the first child a **design-only ticket** (e.g., "Design <feature> screens") that every UI implementation child depends on. Mark it as design-only in the split — it gets the `Design` label in Pass 1, its body includes the `### Design Direction` section from this refinement, it is executed via `/agentflow:design`, and it produces a committed design spec rather than a PR (the one exception to "1 ticket = 1 PR"). When `/agentflow:design` completes it, the `Designed` label is propagated to the implementation children that depend on it, satisfying implement's Design gate.

## Update Ticket

> **CRITICAL**: This section is mandatory after refinement. Do NOT skip it.
> All questions in this section MUST use the `AskUserQuestion` tool — never ask as plain text.

10. **Using `AskUserQuestion`**, ask: "Do you want me to update the ticket in GitHub with this refined information?"

   - If the user says **no** → skip to the label step below (step 12)
   - If the user says **yes** → proceed to step 11

11. **Update the ticket description in the remote system.**

   > **IMPORTANT**: Writing a temp file is NOT updating the ticket. You MUST execute the update command after writing the file. Never stop between writing the temp file and running the update command.

   ```bash
   printf '%s' '<updated description>' > /tmp/claude/issue-<number>.md
   BODY=$(cat /tmp/claude/issue-<number>.md)
   gh issue edit <number> --repo <owner>/<repo> --body "$BODY"
   ```

   **Verify the update succeeded** — re-fetch the ticket and confirm the body was changed:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json body --jq '.body' | head -c 200
   ```

   If the update failed, report the error to the user and retry once.

   If splitting, create the child tickets using a **two-pass approach**:

   Split tickets must also receive the "Refined" label/tag since they were refined during this session — `/implement` checks for it as a pre-flight condition.

   #### Pass 1: Create children with numbered titles and dependency info

   Create children **in dependency order** — independent children first, then children that depend on them (so you have their issue numbers for `Depends on` references).

   Each child title gets a `(K/N)` suffix, e.g. "Add API validation (1/3)".

   Each child body includes:
   - `Related to #<parent>` (links back to parent)
   - `Depends on #<sibling>` lines for any children it depends on (if applicable)
   - `Parallel with #<sibling>` lines for children it can run alongside (if applicable)

   Capture each created issue number from the command output.

   ```bash
   printf '%s' 'Related to #<original-number>
   Depends on #<sibling-number>
   Parallel with #<sibling-number>

   <ticket-body>' > /tmp/claude/issue-child-K.md
   BODY=$(cat /tmp/claude/issue-child-K.md)
   gh issue create --repo <owner>/<repo> --title "<ticket-title> (K/N)" --body "$BODY" --label "Refined"
   ```
   Parse the issue number from the URL in the output (e.g. `https://github.com/owner/repo/issues/10` → `10`).

   Omit `Depends on` / `Parallel with` lines that don't apply (e.g. the first child typically has no dependencies).

   Design-only children (see **Design-first splits** above) additionally get `--label "Design"`, and their body includes the `### Design Direction` section from this refinement (that's where `/agentflow:design` reads it from).

   #### Pass 2: Update parent with tracking section

   After all children are created, re-read the parent ticket's current body and append a `### Child Tickets` section:

   ```markdown
   ### Child Tickets
   - [ ] #10 (1/3): Add API validation
   - [ ] #11 (2/3): Add frontend form — depends on #10
   - [ ] #12 (3/3): Add integration tests — parallel with #11

   **Execution order:** #10 first → then #11 and #12 in parallel
   ```

   Update the parent:
   ```bash
   printf '%s' '<existing-body>

   ### Child Tickets
   <checklist>' > /tmp/claude/issue-<original-number>.md
   BODY=$(cat /tmp/claude/issue-<original-number>.md)
   gh issue edit <original-number> --repo <owner>/<repo> --body "$BODY"
   ```

   #### Companion design ticket (frontend tickets, no split)

   If `designNeeded` is true, the ticket is **not** being split, and `isDesignTicket` is false, create a dedicated design ticket — design never runs on the implementation ticket itself:

   ```bash
   printf '%s' 'Related to #<number>
   Blocks #<number>

   ### Goal
   Produce the design spec (`.pen` + `DESIGN.md`) for #<number> via `/agentflow:design`.

   ### Design Direction
   <the Design Direction section from this refinement>' > /tmp/claude/issue-design.md
   BODY=$(cat /tmp/claude/issue-design.md)
   gh issue create --repo <owner>/<repo> --title "Design: <feature title>" --label "Refined" --label "Design" --body "$BODY"
   ```

   Parse the new issue number `<D>` from the output URL, then append a dependency line to the implementation ticket's body:

   ```
   Depends on #<D> (design)
   ```

   When `/agentflow:design <D>` completes, it closes #<D> and propagates the `Designed` label to this ticket, satisfying implement's Design gate. If the user declined the ticket update in step 10, still offer to create the companion design ticket via `AskUserQuestion` (skip the `Depends on` body edit if declined).

12. **Add the "Refined" label and remove "Working":**
   - If `isDesignTicket` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Design" --remove-label "Working"`
   - Else if `browserRequired` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Browser" --remove-label "Working"`
   - Otherwise:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --remove-label "Working"`
   - If re-refining and `browserRequired` is false but the issue currently has the `Browser` label, also add `--remove-label "Browser"`
   - If re-refining and `isDesignTicket` is false but the issue currently has the `Design` label, also add `--remove-label "Design"`
   - If the user declined the ticket update in step 10, use `AskUserQuestion` to ask: "Do you want me to mark this ticket as Refined?" and apply just the label if yes.

13. **Auto-label `ui:visual-check` for visual/layout tickets** (skip if `isDesignTicket` is true):
   If the ticket description, acceptance criteria, or answers during refinement match the **visual-check signals** subset in the `frontend-classification` reference skill, add the `ui:visual-check` label:
   `gh issue edit <number> --repo <owner>/<repo> --add-label "ui:visual-check"`

   This label signals to the implement skill that interactive browser verification via Playwright CLI should be used.

## After Refinement

**STOP HERE.** Your job is done. Do not:
- Enter plan mode or propose an implementation plan
- Offer to run `/implement` or start implementation
- Suggest next steps beyond what's described above

The user will explicitly invoke `/implement` when they're ready to proceed — or `/agentflow:design` for design-only tickets (labeled `Design`).
