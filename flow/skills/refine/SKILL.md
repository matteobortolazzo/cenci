---
name: refine
description: "Refine a ticket interactively until it is ready for planning."
compatibility: Requires Claude Code AskUserQuestion and cenci project configuration.
argument-hint: <ticket-id> [additional context]
user-invocable: true
disable-model-invocation: true
model: sonnet
allowed-tools: Read, Write, Glob, Task, Bash(gh:*), Bash(git:*), Bash(curl:*), Bash(mkdir:*), Bash(mktemp:*), Bash(cat:*), Bash(rm:*), AskUserQuestion, WebFetch
---

> **Client dispatch**: In Codex, read `codex-runtime` and `refine/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

## Context

Read `project-core` and resolve neutral configuration before continuing.

Use the config returned by `project-core`; if none exists, stop with its client-appropriate setup guidance.

**Parse `$ARGUMENTS`:**
The first token is the ticket ID. Everything after it is optional **user context** (additional instructions or focus areas).

Split `$ARGUMENTS` into:
- **Ticket ID**: the first whitespace-delimited token, with any leading `#` prefix stripped.
  For example: `#1 focus on API` → ID `1`, `7` → ID `7`.
- **User context**: everything after the first token (may be empty).
  For example: `42 focus on the API layer` → context is `focus on the API layer`.

**Ticket ID validation**: Validate the parsed ticket ID against `^[0-9]+$`. If it does not match, **stop immediately** — do not run `gh`, do not write any temp file — and tell the user:
"The ticket ID must be numeric (digits only) — got `<value>`. Re-run `/cenci:refine <ticket-id> [additional context]` with the ticket's numeric ID (e.g. `/cenci:refine 350`)."

**Shell rules**: Read the `shell-rules` skill before running any `gh` commands (covers heredoc temp-file pattern).

**Fetch the ticket:**
Extract owner/repo from `git remote get-url origin` (e.g. `git@github.com:owner/repo.git` → `owner/repo`).

**owner/repo validation**: Trim surrounding whitespace from the derived value (e.g. a trailing newline from command output), then validate it against `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$` (exactly one `/`, each segment limited to letters, digits, `.`, `_`, `-`), additionally rejecting either segment being exactly `.` or `..`, or ending in `.git` (GitHub never allows a repo name to end in `.git`, so a trailing `.git` here always indicates a botched extraction, not a real repo name). If it does not match, **stop immediately** — do not run `gh`, do not write any temp file. Before echoing the value below, redact anything up through the last `@` (keep only the text after it, or the whole value if no `@` is present) so a malformed remote URL with embedded userinfo/credentials is never echoed back — then tell the user:
"The derived `owner/repo` value (`<value>`) does not look like a valid `owner/repo`. Check `git remote get-url origin` and verify the remote URL is correctly configured."

Once validated, run:
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
| Has "Planned" label/tag | "This ticket already has a saved plan (Planned). Do you want to re-refine it?" |
| Has "Working" label/tag | "This ticket is currently being worked on. Do you want to re-refine it?" |
| Has "In Review" label/tag | "This ticket has an open PR (In Review). Do you want to re-refine it?" |
| Has "Implemented" label/tag | "This ticket is already marked as Implemented (its PR merged). Do you want to re-refine it?" |

If the user says no → stop. If yes → proceed normally.

## Ticket Ownership

Read the `ticket-ownership` reference skill and follow it using the assignees from
the ticket fetch above. Complete its claim-and-verify contract before adding
`Working` or starting refinement. Never replace an existing assignee.

## Label "Working"

**Before starting refinement work**, add the "Working" label to signal that the ticket is actively being worked on. `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure it exists first — run each as its own Bash call (`|| true` swallows only the "already exists" error):
```bash
gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "Actively being refined, designed, or implemented" 2>/dev/null || true
```
```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Working"
```
Apply the same ensure-then-add pattern to every label this skill applies later (`Refined`, `Design`, `ui:visual-check`, …): before the first `--add-label <name>` of a label, run its `gh label create <name> … || true` with the color/description from the lifecycle table in `/cenci:configure`.

## Your Role

You orchestrate backlog refinement. The judgment-heavy analysis — ambiguity hunting, question drafting, sizing, and the refined ticket proposal — is delegated to the **refiner** agent (spawned via the `Task` tool), whose `model: opus` frontmatter pin holds for its entire run; a skill-level pin only lasts the invoking turn, so it would silently degrade every follow-up turn of the Q&A loop to the session model. You run the interaction and the writes: relay the refiner's questions to the user, feed answers back, and perform every GitHub mutation yourself. Never perform the refiner's analysis inline, and never delegate a GitHub write or an `AskUserQuestion` to a subagent (see the `subagent-safety` skill).

## Process

1. **Summarize** the fetched ticket for the user in 2-3 lines (title, current scope, state). Do not analyze it — that is the refiner's job.

2. **Classify ticket type**: Read the `frontend-classification` reference skill and apply its rule to determine if this ticket involves frontend/UI work. Record the result as `isFrontend` for the bundle and the labeling steps.

   **Design-only classification** (if `isFrontend` AND `pencil.enabled` is `true` in `.cenci/config.json`): determine whether the ticket's *deliverable* is the design itself — a `.pen` file plus `DESIGN.md` spec, with no production code change (e.g., "Design the settings page", "Create mockups for the onboarding flow"). If the signals point that way, confirm via `AskUserQuestion`:

   > "This reads as a design-only ticket — the deliverable would be a design spec (`.pen` + `DESIGN.md`) produced by `/cenci:design`, with no code change. Is that right?"

   Options: "Yes — design-only", "No — includes implementation"

   If confirmed, set `isDesignTicket = true`. Design-only tickets are routed to `/cenci:design`, not `/cenci:implement`: they skip the browser question (step 8) and the `ui:visual-check` label (step 12), and receive the `Design` label in step 11. The refiner focuses its analysis accordingly via the bundle's `isDesignTicket` flag — design questions (visual direction, screens, states, design-system fit) instead of implementation-only items.

   The **Design Coverage Check** (`.pen`/`DESIGN.md` evaluation and the `designNeeded` determination) is performed by the refiner agent, not here — its result arrives in the proposal's `### Design Coverage` section (step 7). **Design always happens on a dedicated design ticket, never on the implementation ticket itself** — when `designNeeded` is true, the design ticket is created later, either as the first child of a split (see **Design-first splits** in step 9) or as a companion ticket (see **Companion design ticket** in the Update Ticket section).

3. **Create the per-run temp-file token.** Run `mktemp -u /tmp/claude/issue-<number>-XXXXXX` once and capture the trailing random segment as `<token>` (the token is the random suffix only, e.g. `a1b2c3` — not the full mktemp basename). As with `<ticket-id-or-slug>` in the implement phases, carry the literal `<token>` value forward as text into every temp-file path for the rest of this run — do NOT re-derive it per Bash call, and do not use `$$`/shell state (it does not persist across separate Bash tool invocations). `-u` is a dry-run name generator — it only produces a unique-ish suffix, not an atomically-created file — which is why the `Write` tool is what actually creates each temp file in this run. The `<token>` is a **collision-avoidance mechanism only** — it reduces the chance of two concurrent runs picking the same temp-file basename — and is explicitly **not** an atomic reservation (a second run could theoretically generate the same suffix before either run's `Write` call lands) and **not** a security boundary (it provides no protection against a malicious or adversarial process targeting the same path).

4. **Write the context bundle.** Use the `Write` tool to create `/tmp/claude/issue-<number>-<token>-bundle.md` containing, in order:
   - The **verbatim** ticket title, body, labels, state, and comments from the fetch above — full text, never a digest or paraphrase (the refiner's decisions require source fidelity; see `docs/skill-authoring.md`).
   - Each attachment's summary alongside its downloaded file path (from the Attachments step).
   - The user context parsed from `$ARGUMENTS`, verbatim (or `None`).
   - The resolved flags: `isFrontend`, `isDesignTicket`, `pencil.enabled`, `pencil.designPath` (from the resolved config).

   If the `Write` fails, retry once; if it still fails, STOP and report the error — the refiner must never run without the bundle.

5. **Delegate to the refiner agent** (`Task` tool, agent `refiner`). The first invocation's prompt contains only: the bundle path, a note that this is round 1, and the instruction to read the bundle file in full before analyzing. Later rounds additionally carry the Q&A history (step 6) — do not re-paste ticket or bundle content into any prompt; the bundle path is the context.

6. **Q&A relay loop.** Parse the refiner's `## Questions` section:
   - If it is `None.` → the same output contains the `## Refined Ticket Proposal`; continue to step 7.
   - Otherwise, present the questions to the user **in the refiner's order**. Ask exactly ONE question per `AskUserQuestion` call, and wait for the answer before the next call. Never combine multiple refiner questions into a single `AskUserQuestion` call, never merge them into one composite question, and never ask them as plain text. When the refiner supplied answer options for a question, map them onto the `AskUserQuestion` options using the refiner's wording (the user can always answer freeform via "Other"); otherwise offer sensible options.
   - When every question in the round is answered, re-invoke the refiner with the bundle path and the **complete** accumulated Q&A history — all rounds, as `Q:`/`A:` pairs using the refiner's original question wording; do not re-paste ticket or bundle content. Route the new output through this step again. The refiner asks at most 4 questions per round and rounds continue until it returns `None.`.

7. **Proposal received.** When the refiner returns `None.`, its output contains the `## Refined Ticket Proposal` — the summary content adopted in step 9 and persisted in steps 10-12. Read `designNeeded` from its `### Design Coverage` section (treat it as false when the section or field is absent).

8. **Before adopting the proposal**, ask one final infrastructure question — but only when it can plausibly apply. **Skip for design-only tickets** (`isDesignTicket` is true) — they never reach the implement pipeline; set `browserRequired: false`. Ask it if the ticket was classified frontend/UI in step 2, **or** the ticket/answers mention web scraping, browser automation, or manual browser testing. For pure backend/infrastructure/data tickets with none of those signals, skip the question and set `browserRequired: false`.

   Using `AskUserQuestion`:
   "Does this story need interactive browser access during implementation? (e.g., for visual verification, form testing, or web scraping). If yes, the implementer should ensure `playwright-cli` is installed (`npm i -g @playwright/cli`)."
   - If **yes** → note `browserRequired: true` for the labeling step
   - If **no** → proceed normally

9. **When refined**, adopt the refiner's `## Refined Ticket Proposal` **verbatim** as the summary content — its sections (`### Updated Title` (optional), `### Updated Description`, `### Acceptance Criteria`, `### Technical Notes`, `### Design Coverage`, `### Design Direction`, `### Size Estimate`, `### Suggested Split` with `#### Execution Order`) map 1:1 onto the persistence steps below; the section formats themselves are specified in `agents/refiner.md`. Do not rewrite, summarize, or reorder the proposal's content. It is not shown yet — steps 10-12 first persist the ticket update, any split or companion design ticket, and the labels; the summary is then presented in the final message together with a notice of what was persisted (see the **Final Message** note at the end of the Update Ticket section).

   A `### Suggested Split` in the proposal means each child becomes its own numbered ticket and PR, with the parent tracking all children and their dependencies (Pass 1/Pass 2 below).

   **Design-first splits** (if frontend feature AND `pencil.enabled` is `true` AND `designNeeded` is true): the proposal's split makes the first child a **design-only ticket** (e.g., "Design <feature> screens") that every UI implementation child depends on. It gets the `Design` label in Pass 1, its body includes the `### Design Direction` section from the proposal (that's where `/cenci:design` reads it from), it is executed via `/cenci:design`, and it produces a committed design spec rather than a PR (the one exception to "1 ticket = 1 PR"). When `/cenci:design` completes it, the `Designed` label is propagated to the implementation children that depend on it, satisfying implement's Design gate.

## Update Ticket

> **CRITICAL**: This section is mandatory after refinement. Do NOT skip it.
> This section runs unconditionally after refinement — there is no confirmation prompt before writing. The human gate is the Q&A loop (steps 6-8); review happens after the writes, via the final message.

> **Write-failure protocol**: Every *edit* in this section (ticket body/title updates, parent tracking updates, label add/remove) MUST be verified by re-fetching the resource with `gh issue view ... --json ...` and confirming the expected change is actually present — a command exiting 0 is not sufficient proof. Ticket *creation* (`gh issue create`) is the one exception: the returned issue URL is itself sufficient proof of success, so no separate re-fetch is required there. This protocol also covers the `TITLE=$(cat <path>)` read-back that precedes a title write — if `cat` fails (missing file) or reads back empty content, treat it exactly like a failed write: do not proceed to `gh` with a blank or stale `--title`. If the write, the read-back, or the verification fails:
> 1. Report the error to the user.
> 2. Retry the write once, then verify again.
> 3. If it still fails, **STOP** — do not proceed to the next step — and emit a partial-state report: what succeeded so far (with concrete issue/label numbers or names), what failed, and what the user needs to do manually to reconcile it. Each write point below states what belongs in that report.

**Per-run temp-file token**: The `<token>` used in every temp-file path of this section is the one created in Process step 3 — carry that same literal value forward; never mint a second token mid-run (two tokens in one run would orphan the earlier files from step 13's cleanup list).

10. **Update the ticket description in the remote system.**

   > **IMPORTANT**: Writing a temp file is NOT updating the ticket. You MUST execute the update command after writing the file. Never stop between writing the temp file and running the update command.

   Use the `Write` tool to create `/tmp/claude/issue-<number>-<token>.md` with the `<updated description>` as its content.

   **Only when the proposal adopted in step 9 includes an `### Updated Title`**, also use the `Write` tool to create `/tmp/claude/issue-<number>-<token>-title.txt` with the raw updated title text as its content — the title is free text and must never be interpolated directly into the command line (a title containing `$(…)`, backticks, or quotes would be shell-interpreted). Then run:
   ```bash
   TITLE=$(cat /tmp/claude/issue-<number>-<token>-title.txt) && [ -n "$TITLE" ] && gh issue edit <number> --repo <owner>/<repo> --body-file /tmp/claude/issue-<number>-<token>.md --title "$TITLE"
   ```

   Otherwise (no title change), run:
   ```bash
   gh issue edit <number> --repo <owner>/<repo> --body-file /tmp/claude/issue-<number>-<token>.md
   ```

   **Verify the update succeeded** — re-fetch the ticket and confirm the body (and, when retitled, the title) changed:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json title,body --jq '.title, (.body[:200])'
   ```

   If the update or verification failed, follow the write-failure protocol: report the error, retry once, and if still failing, STOP — do not create children, do not run Pass 2, do not create the companion design ticket, do not proceed to steps 11-12 — and report to the user that ticket #`<number>`'s description/title update did not persist, so they can retry manually.

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

   Use the `Write` tool to create `/tmp/claude/issue-<number>-<token>-child-K.md` with the following content:
   ```
   Related to #<original-number>
   Depends on #<sibling-number>
   Parallel with #<sibling-number>

   <ticket-body>
   ```
   Also use the `Write` tool to create `/tmp/claude/issue-<number>-<token>-child-K-title.txt` with the raw title text `<ticket-title> (K/N)` as its content — the title is free text and must never be interpolated directly into the command line. Then run:
   ```bash
   TITLE=$(cat /tmp/claude/issue-<number>-<token>-child-K-title.txt) && [ -n "$TITLE" ] && gh issue create --repo <owner>/<repo> --title "$TITLE" --body-file /tmp/claude/issue-<number>-<token>-child-K.md --label "Refined"
   ```
   Parse the issue number from the URL in the output (e.g. `https://github.com/owner/repo/issues/10` → `10`) — this is the success confirmation for this child.

   If creation fails for a child, follow the write-failure protocol: report the error, retry once, and if still failing, STOP — do not create any further children, and do not run Pass 2. Report the partial state to the user: which children (if any) were already created, with their issue numbers.

   Omit `Depends on` / `Parallel with` lines that don't apply (e.g. the first child typically has no dependencies).

   Design-only children (see **Design-first splits** above) additionally get `--label "Design"`, and their body includes the `### Design Direction` section from this refinement (that's where `/cenci:design` reads it from).

   #### Pass 2: Update parent with tracking section

   After all children are created, re-read the parent ticket's current body and append a `### Child Tickets` section:

   ```markdown
   ### Child Tickets
   - [ ] #10 (1/3): Add API validation
   - [ ] #11 (2/3): Add frontend form — depends on #10
   - [ ] #12 (3/3): Add integration tests — parallel with #11

   **Execution order:** #10 first → then #11 and #12 in parallel
   ```

   Update the parent. Use the `Write` tool to create `/tmp/claude/issue-<original-number>-<token>.md` with the following content (this uses `<original-number>` — parent == original — with the SAME run token from step 10, not a new one):
   ```
   <existing-body>

   ### Child Tickets
   <checklist>
   ```
   Then run:
   ```bash
   gh issue edit <original-number> --repo <owner>/<repo> --body-file /tmp/claude/issue-<original-number>-<token>.md
   ```

   **Verify** by re-fetching the parent and confirming the `### Child Tickets` section is present in the body:
   ```bash
   gh issue view <original-number> --repo <owner>/<repo> --json body --jq '.body'
   ```

   If the update or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that children #`<c1>`, #`<c2>`, … exist but the parent ticket #`<original-number>` is not yet tracking them, so the user can append the `### Child Tickets` section manually.

   #### Companion design ticket (frontend tickets, no split)

   If `designNeeded` is true, the ticket is **not** being split, and `isDesignTicket` is false, create a dedicated design ticket — design never runs on the implementation ticket itself:

   Use the `Write` tool to create `/tmp/claude/issue-<number>-<token>-design.md` with the following content:
   ```
   Related to #<number>
   Blocks #<number>

   ### Goal
   Produce the design spec (`.pen` + `DESIGN.md`) for #<number> via `/cenci:design`.

   ### Design Direction
   <the Design Direction section from this refinement>
   ```
   Also use the `Write` tool to create `/tmp/claude/issue-<number>-<token>-design-title.txt` with the raw title text `Design: <feature title>` as its content — the title is free text and must never be interpolated directly into the command line. Then run:
   ```bash
   TITLE=$(cat /tmp/claude/issue-<number>-<token>-design-title.txt) && [ -n "$TITLE" ] && gh issue create --repo <owner>/<repo> --title "$TITLE" --label "Refined" --label "Design" --body-file /tmp/claude/issue-<number>-<token>-design.md
   ```

   If creation fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that the design ticket was not created, so the implementation ticket's body cannot be updated with a dependency line.

   Parse the new issue number `<D>` from the output URL, then append a dependency line to the implementation ticket's body. Use the `Write` tool to create `/tmp/claude/issue-<number>-<token>.md` (reusing the bare `issue-<number>-<token>.md` path from step 10, not a `-design` suffixed one) with the implementation ticket's current body plus an appended:

   ```
   Depends on #<D> (design)
   ```

   Then run:
   ```bash
   gh issue edit <number> --repo <owner>/<repo> --body-file /tmp/claude/issue-<number>-<token>.md
   ```

   **Verify** by re-fetching the implementation ticket and confirming the `Depends on #<D> (design)` line is present in the body:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json body --jq '.body'
   ```

   If the update or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that the implementation ticket #`<number>` was not updated with the `Depends on #<D> (design)` line — but design ticket #`<D>` exists, so the user can add the dependency line manually.

   When `/cenci:design <D>` completes, it closes #<D> and propagates the `Designed` label to this ticket, satisfying implement's Design gate.

11. **Add the "Refined" label and remove "Working":**
   - If `isDesignTicket` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Design" --remove-label "Working"`
   - Else if `browserRequired` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Browser" --remove-label "Working"`
   - Otherwise:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --remove-label "Working"`
   - If re-refining and `browserRequired` is false but the issue currently has the `Browser` label, also add `--remove-label "Browser"`
   - If re-refining and `isDesignTicket` is false but the issue currently has the `Design` label, also add `--remove-label "Design"`

   **Verify** by re-fetching the issue's labels and confirming the expected set is present/absent:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json labels --jq '.labels[].name'
   ```

   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report which labels did and didn't apply on ticket #`<number>`.

12. **Auto-label `ui:visual-check` for visual/layout tickets** (skip if `isDesignTicket` is true):
   If the ticket description, acceptance criteria, or answers during refinement match the **visual-check signals** subset in the `frontend-classification` reference skill, add the `ui:visual-check` label:
   `gh issue edit <number> --repo <owner>/<repo> --add-label "ui:visual-check"`

   **Verify** by re-fetching the issue's labels and confirming `ui:visual-check` is present:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json labels --jq '.labels[].name'
   ```

   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that `ui:visual-check` did not apply to ticket #`<number>`.

   This label signals to the implement skill that interactive browser verification via Playwright CLI should be used.

   **Mark this run complete.** Once step 12 has taken its action or been correctly skipped (`isDesignTicket` is true), and the write-failure protocol has not STOPped anywhere in steps 10-12, use the `Write` tool to create an empty file at `/tmp/claude/issue-<number>-<token>.ok`. This marker is what step 13 checks before deleting anything. Confirm the write succeeded by re-reading it with `cat /tmp/claude/issue-<number>-<token>.ok` — if that `cat` errors, treat the marker write itself as failed (report it as such, distinct from a steps-10-12 failure) rather than letting step 13 silently read it as "absent" for an unrelated reason.

13. **Clean up this run's scoped temp files.**

   Check whether this run completed successfully by attempting to read the marker file:
   ```bash
   cat /tmp/claude/issue-<number>-<token>.ok
   ```
   If the command errors (non-zero exit, e.g. `No such file or directory`), the marker is absent. If it exits 0 (silently, since the marker is an empty file), the marker is present.

   **If the marker is present** — every write in steps 10-12 succeeded and was verified, so it's safe to delete this run's temp files by explicit path (never a glob — an unmatched glob errors under some shells, and `rm -f` already ignores paths that don't exist, so listing files a given run didn't create — e.g. child/design files when there was no split or companion design ticket — is harmless):
   ```bash
   rm -f \
     /tmp/claude/issue-<number>-<token>.md \
     /tmp/claude/issue-<number>-<token>-bundle.md \
     /tmp/claude/issue-<number>-<token>-title.txt \
     /tmp/claude/issue-<number>-<token>-design.md \
     /tmp/claude/issue-<number>-<token>-design-title.txt \
     /tmp/claude/issue-<number>-<token>-child-K.md \
     /tmp/claude/issue-<number>-<token>-child-K-title.txt \
     /tmp/claude/issue-<number>-<token>.ok
   ```
   Repeat the child-K paths (with the actual `K` value substituted) for each child created in Pass 1 this run.

   **If the marker is absent** — an earlier step in 10-12 did not complete successfully (the write-failure protocol already STOPped before reaching this step). Skip cleanup entirely and state explicitly to the user that cleanup was skipped for this reason, preserving the run's `<token>`-scoped temp files for manual recovery.

### Final Message

After steps 10-13 complete, present the refiner's Refined Ticket Proposal adopted in step 9 in the final message, followed by a short notice of what was persisted:

> Ticket #`<n>` updated. Labels: Refined[, Design][, Browser][, ui:visual-check]. [Created `N` child tickets: #`<c1>`, #`<c2>`, ….] [Created companion design ticket #`<D>`.]
>
> Review the summary above.

Do not display the summary earlier in the flow — it is shown once, here, after all writes complete. Do not name or suggest the next command to run (`/implement`, `/cenci:design`, etc.) here — that's covered by the **After Refinement** section below.

## After Refinement

**STOP HERE.** Your job is done. Do not:
- Enter plan mode or propose an implementation plan
- Offer to run `/implement` or start implementation
- Suggest next steps beyond what's described above

The user will explicitly invoke `/implement` when they're ready to proceed — or `/cenci:design` for design-only tickets (labeled `Design`).
