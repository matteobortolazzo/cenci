---
name: refine
description: "Refine a ticket interactively until it is ready for planning."
compatibility: Requires Claude Code AskUserQuestion and cenci project configuration.
argument-hint: <ticket-id> [additional context]
user-invocable: true
disable-model-invocation: true
model: sonnet
allowed-tools: Read, Write, Glob, Task, Bash(gh issue view:*), Bash(gh issue edit:*), Bash(gh label create:*), Bash(gh api user --jq:*), Bash(gh api repos/:*), Bash(git remote get-url:*), Bash(jq -n:*), Bash(mktemp -u ${TMPDIR:-/tmp}/cenci/:*), Bash(cat ${TMPDIR:-/tmp}/cenci/:*), Bash(rm -f ${TMPDIR:-/tmp}/cenci/:*), AskUserQuestion
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
Apply the same ensure-then-add pattern to every label this skill applies later (`Refined`, `Design`, `Browser`, `ui:visual-check`, `automerge:ok`, …): before the first `--add-label <name>` of a label, run its `gh label create <name> … || true` with the color/description from the lifecycle table in `/cenci:configure`.

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

3. **Create the per-run temp-file token.** Run `mktemp -u ${TMPDIR:-/tmp}/cenci/issue-<number>-XXXXXX` once and capture the trailing random segment as `<token>` (the token is the random suffix only, e.g. `a1b2c3` — not the full mktemp basename). As with `<ticket-id-or-slug>` in the implement phases, carry the literal `<token>` value forward as text into every temp-file path for the rest of this run — do NOT re-derive it per Bash call, and do not use `$$`/shell state (it does not persist across separate Bash tool invocations). `-u` is a dry-run name generator — it only produces a unique-ish suffix, not an atomically-created file — which is why the `Write` tool is what actually creates each temp file in this run. The `<token>` is a **collision-avoidance mechanism only** — it reduces the chance of two concurrent runs picking the same temp-file basename — and is explicitly **not** an atomic reservation (a second run could theoretically generate the same suffix before either run's `Write` call lands) and **not** a security boundary (it provides no protection against a malicious or adversarial process targeting the same path).

4. **Write the context bundle.** Use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md` containing, in order:
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

9. **When refined**, adopt the refiner's `## Refined Ticket Proposal` **verbatim** as the summary content — its sections (`### Updated Title` (optional), `### Updated Description`, `### Acceptance Criteria`, `### Assumptions (auto-adopted)`, `### Decisions`, `### Technical Notes`, `### Design Coverage`, `### Design Direction`, `### Automation`, `### Size Estimate`, `### Suggested Split` with `#### Execution Order`) map 1:1 onto the persistence steps below; the section formats themselves are specified in `agents/refiner.md`. `### Assumptions (auto-adopted)` and `### Decisions` persist into the ticket body in step 10, same as `### Acceptance Criteria`; `### Automation` drives the `## Confirmation Gate`'s per-ticket `automerge:ok` verdict computation (parent and every child) and is never written into the body. Do not rewrite, summarize, or reorder the proposal's content. The proposal itself is rendered to the user at the `## Confirmation Gate` below, before any write — it is not held back until the final message; steps 10-13 persist the ticket update, any split or companion design ticket, and the labels only after that gate's Confirm branch (see the **Final Message** note at the end of the Update Ticket section).

   A `### Suggested Split` in the proposal means each child becomes its own numbered ticket and PR, linked to the parent as a native GitHub sub-issue, with dependency ordering captured in the child bodies (Pass 1/Pass 2 below).

   **Design-first splits** (if frontend feature AND `pencil.enabled` is `true` AND `designNeeded` is true): the proposal's split makes the first child a **design-only ticket** (e.g., "Design <feature> screens") that every UI implementation child depends on. It gets the `Design` label in Pass 1, its body includes the `### Design Direction` section from the proposal (that's where `/cenci:design` reads it from), it is executed via `/cenci:design`, and it produces a committed design spec rather than a PR (the one exception to "1 ticket = 1 PR"). When `/cenci:design` completes it, the `Designed` label is propagated to the implementation children that depend on it, satisfying implement's Design gate.

## Confirmation Gate

Placed here — between Process step 9 (the proposal is adopted) and `## Update Ticket` below — this gate performs **zero** GitHub writes; it only classifies, asks, computes, and renders. **No proposal-related GitHub write occurs before** its single confirmation. Steps 10-13 below then consume everything this gate computes; they never recompute a verdict or a label set.

1. **Per-child classification** — only when the adopted proposal carries a `### Suggested Split`. For each proposed child K, apply the `frontend-classification` reference skill to **that child's own block text** (its title, `### Goal`, and `### Acceptance criteria`) — never an inlined keyword list; that skill is the single source of truth. Record `childIsFrontend(K)` and `childVisualCheck(K)` (the visual-check signal subset) per child.

2. **Per-child browser question** — for each child where `childIsFrontend(K)` is true, or whose block mentions web scraping, browser automation, or manual browser testing, ask step 8's question once, scoped to that child, as its own `AskUserQuestion` call:
   "Does child (K/N) `<title>` need interactive browser access during implementation? (e.g., for visual verification, form testing, or web scraping). If yes, the implementer should ensure `playwright-cli` is installed (`npm i -g @playwright/cli`)."
   Ask them in child order (1/N … N/N); never combine two children into one call — mirrors step 6's one-question-per-call rule. **Skip entirely for a design-only child** — design children never reach the implement pipeline, exactly as step 8 skips for `isDesignTicket`, and `childBrowserRequired(K)` is set `false`. A child with no frontend/browser signal is not asked at all and gets `childBrowserRequired(K) = false`.

3. **The parent's step-8 question is independent of every child.** Step 8 stays where it is and is unchanged; its answer applies to the parent ticket only and **is never propagated to any child** — on a split the parent is a tracking epic, so a `browserRequired: true` parent answer must not force `Browser` or a withhold onto a child, and a `false` parent answer must not clear a child's own `true`.

4. **Compute effective verdicts**, per ticket T in {parent, child 1…N}:
   `effective grant(T) = ### Automation verdict for T is exactly "grant" AND NOT isDesignTicket AND NOT browserRequired AND NOT visual-check-signals-match` (each override evaluated for T itself — the parent's own `isDesignTicket`/`browserRequired`/visual-check match for the parent; for a child K, `isDesignTicket(K)` is true only when K is the design-only child produced by a Design-first split (the child seeded with `["Refined","Design"]` in Pass 1) — never derived from or equal to `childIsFrontend(K)` — plus that child's own `childBrowserRequired(K)` and `childVisualCheck(K)`).
   **Fail-closed default preserved**: an absent/empty `### Automation` entry for T, or any value other than exactly `grant`, is `withhold`.

5. **Compute the label set per ticket**: the parent's set is computed per steps 11-12 below (unchanged in shape; steps 11-12 consume the gate-computed values rather than recomputing them); each child's set = inherited non-excluded parent labels + `Refined` [+ `Design` for the design child] [+ `Browser` when `childBrowserRequired(K)`] [+ `ui:visual-check` when `childVisualCheck(K)` and the child is not the design child] [+ `automerge:ok` when its effective grant holds]. The "inherited non-excluded parent labels" shown here and in the manifest below are a preview: they are resolved definitively at write time by step 10's own parent-metadata fetch (the **Parent metadata for inherited labels/milestone** sub-step, run just before Pass 1), which may see a different label set if the parent changed between the gate and the write — this is cosmetic-label drift only. The gate's own `automerge:ok`/`Browser`/`ui:visual-check` computation above is authoritative and does not change at write time.

6. **Render** the complete adopted proposal, followed by a manifest: one row per ticket to be updated or created, with its title, final label set, and `grant`/`withhold` plus a one-line rationale. If the parent already carries sub-issues from a prior run, state in the manifest that those existing children will not be modified — and note that a child created before this gate existed may still carry a legacy `automerge:ok`/`Browser`/`ui:visual-check` grant inherited under the old behavior; this run does not audit or revoke it, since the unmodified child is untouched by design.

7. **One confirmation.** Ask, via `AskUserQuestion`:
   "Apply this refinement as shown?"
   Options: "Confirm — apply as shown" / "Decline — make no changes". No adjust loop — a decline requires a fresh `/cenci:refine <id>` run to change anything.

8. **Decline** ⇒ no ticket, label, or sub-issue mutation of any kind; jump straight to step 13's declined-cleanup branch below, and report that `Working` and the assignee claim remain (removing them would itself be a mutation the Decisions section forbids) and that re-running `/cenci:refine <id>` is how to adjust the proposal.

**Confirm** ⇒ continue into `## Update Ticket` below, where steps 10-13 apply every effective verdict and label set exactly as rendered in the manifest.

## Update Ticket

> **CRITICAL**: This section is mandatory after refinement. Do NOT skip it.
> **No proposal-related GitHub write occurs before** the human confirms at the `## Confirmation Gate` above — that gate, not the Q&A loop (steps 6-8), is the pre-write authorization boundary. The Q&A loop only shapes the proposal's content; steps 10-13 below execute only after the gate's Confirm branch, and a Decline stops before any of them run (see step 13's declined branch).

> **Write-failure protocol**: Every *edit* in this section (ticket body/title updates, parent tracking updates, label add/remove) MUST be verified by re-fetching the resource with `gh issue view ... --json ...` and confirming the expected change is actually present — a command exiting 0 is not sufficient proof. Ticket *creation* is the one exception: `--jq .number` on the `gh api repos/...` response returns the new issue number directly — a numeric value is the proof; empty output, non-numeric output, or a non-zero exit is a failed create, so no separate re-fetch is required there. A malformed JSON payload surfaces as an API 4xx parse error and is itself a failed write — handle it with the single documented retry below, not a hand-patch loop. This retry also covers the local `Write` tool call that authors the raw title/body files and the `jq -n --rawfile` invocation that composes the JSON payload from them (see the `shell-rules` skill's canonical snippet): if a raw-file `Write` call fails, or the subsequent `jq` invocation exits non-zero, or the payload file is missing/empty/stale when the `gh api repos/... --input` command is about to read it, retry the failed step (`Write` or `jq`) once before (re-)invoking that `gh api repos/...` command — do not assume a local Write or jq failure is instead an API-side rejection. If the write or the verification fails:
> 1. Report the error to the user.
> 2. Retry the write once, then verify again.
> 3. If it still fails, **STOP** — do not proceed to the next step — and emit a partial-state report: what succeeded so far (with concrete issue/label numbers or names), what failed, and what the user needs to do manually to reconcile it. Each write point below states what belongs in that report.

**Per-run temp-file token**: The `<token>` used in every temp-file path of this section is the one created in Process step 3 — carry that same literal value forward; never mint a second token mid-run (two tokens in one run would orphan the earlier files from step 13's cleanup list).

**Ensure gate-decided labels exist, before Pass 1.** Step 10's Pass 1 below may seed `automerge:ok`, `Browser`, and/or `ui:visual-check` directly into a child's create payload, and step 11 may add `automerge:ok` to the parent — `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure all three exist now, before any child is created (the ensure-then-add rule already exists at `SKILL.md:87`; this only relocates it ahead of the writes that need it, using the same colors/descriptions as the canonical table at `configure/SKILL.md:715`). Run each ensure as its own Bash call — idempotent (`|| true`), so running one this run doesn't need is harmless:
```bash
gh label create "automerge:ok" --repo <owner>/<repo> --color "006B75" --description "Human granted hands-off merge at refinement — babysit may merge this PR without review" 2>/dev/null || true
```
```bash
gh label create "Browser" --repo <owner>/<repo> --color "BFD4F2" --description "Implementation needs interactive browser access (Playwright CLI)" 2>/dev/null || true
```
```bash
gh label create "ui:visual-check" --repo <owner>/<repo> --color "FEF2C0" --description "Visual/layout change — verify in a browser before merge" 2>/dev/null || true
```

10. **Update the ticket description in the remote system.**

   > **IMPORTANT**: Writing a temp file is NOT updating the ticket. You MUST execute the update command after writing the file. Never stop between writing the temp file and running the update command.

   **When the proposal adopted in step 9 includes an `### Updated Title`**, use the `Write` tool to create the raw title and body as plain text — `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-title.txt` (the updated title) and `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-body.md` (the updated description) — never a hand-escaped JSON literal (the title is free text and must never be interpolated directly into a command line; a title containing `$(…)`, backticks, or quotes would be shell-interpreted). Build the payload per the `shell-rules` skill's canonical `jq -n --rawfile` snippet:
   ```bash
   jq -n --rawfile title ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-title.txt --rawfile body ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-body.md '{title: ($title | rtrimstr("\n")), body: $body}' > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit.json
   ```
   Then run:
   ```bash
   gh api repos/<owner>/<repo>/issues/<number> -X PATCH --input ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit.json
   ```

   Otherwise (no title change), use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.md` with the `<updated description>` as its content, then run:
   ```bash
   gh issue edit <number> --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.md
   ```

   `<updated description>` is not description-only: the body file's content is the concatenation of every ticket-body section the proposal carries — `### Updated Description`, `### Acceptance Criteria`, `### Assumptions (auto-adopted)`, `### Decisions`, `### Technical Notes`, plus `### Design Coverage`/`### Design Direction` when present — in that order; `### Automation` is never included.

   **Verify the update succeeded** — re-fetch the ticket and confirm the body (and, when retitled, the title) changed. Compare against a meaningfully wide slice of the body, not just its opening — mid-body corruption from an escaping mistake could otherwise land past a short truncation and go unnoticed:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json title,body --jq '.title, (.body[:2000])'
   ```

   If the update or verification failed, follow the write-failure protocol: report the error, retry once, and if still failing, STOP — do not create children, do not run Pass 2, do not create the companion design ticket, do not proceed to steps 11-12 — and report to the user that ticket #`<number>`'s description/title update did not persist, so they can retry manually.

   #### Parent metadata for inherited labels/milestone

   Every ticket this skill creates — each split child **and** the companion design ticket — inherits the parent's milestone and its non-lifecycle labels, so a split never drops its children out of the milestone or loses the parent's `area:*`/priority/team labels. Skip this sub-step entirely when this run creates neither split children nor a companion design ticket; there is nothing to inherit into.

   Fetch the parent's milestone and labels **to a file**, as its own Bash call (a fetch failure then surfaces distinctly, before any create), mirroring the same pattern in `implement/phases/phase-9-pr.md`. Do not reuse the `labels`/`milestone` already in context from the Context section's fetch: they are stale (this skill added `Working` to the parent after that fetch), and externally-sourced label names must never be interpolated into a command line (`docs/skill-authoring.md`) — the file is consumed mechanically by `jq --slurpfile` below, never read back into shell text. The trailing `|| rm -f` is load-bearing: it removes a partially-written file so the presence gate below can never mistake one for a good fetch.
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json milestone,labels > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json || rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json
   ```

   **Presence gate** — choose between the **with-meta** and **no-meta** payload forms at every creation site below by reading the file back:
   ```bash
   cat ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json
   ```
   If the command errors (non-zero exit, e.g. `No such file or directory`), the metadata is absent → use the **no-meta** form. If it exits 0 and prints the JSON object, the metadata is present → use the **with-meta** form.

   **On failure: retry once, then graceful-degrade — do NOT STOP.** If the fetch fails, retry it once. If it still fails, fall through to the **no-meta** form at every creation site and record that the **Final Message** must state that `milestone/label inheritance was skipped`. This deliberately differs both from the write-failure protocol governing every other write in this section and from the fail-closed treatment of the same fetch at `implement/phases/phase-9-pr.md` (whose risk was a runaway followup chain, not a missing label): inheritance here is cosmetic — a child created without the parent's milestone is still a correct, complete ticket — and STOPping at this point would abort the split *after* the parent's body was already rewritten, which is strictly worse than an un-inherited child.

   **What is inherited.** Carry over every label the parent currently has except the 10 lifecycle/transient and per-ticket-grant markers — `"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"` — which are per-ticket state or per-ticket grants, never classification. The labels this skill seeds (`Refined`, plus `Design` for a design-only child or the companion design ticket) are applied on top regardless of what is carried over; each child's own earned `automerge:ok`/`Browser`/`ui:visual-check` (computed at the `## Confirmation Gate` above) are appended separately, per child, in Pass 1 below — never inherited from the parent's current labels. Two invariants carry over verbatim from phase-9's already-tested form, and are the non-obvious parts:
   - the milestone must be the numeric `.milestone.number`, not the title — the REST endpoint requires the id;
   - the `milestone` key is omitted entirely via an explicit jq emptiness check, never a bare `//` fallback that would emit `null` (see `docs/shell-scripting-gotchas.md`).

   **`automerge:ok`, `Browser`, and `ui:visual-check` are never inherited.** A split child, the companion design ticket, and (per the followup-creation sites this same ticket also narrows) a followup ticket never inherit a parent's hands-off-merge grant or its browser/visual-check markers — each child's verdict and label set is instead applied explicitly from the gate, independently, at Pass 1 create time (steps 4-5 of the Confirmation Gate above). This closes the leak the old inheritance behavior had: a pre-existing `automerge:ok` already on the parent from a prior run is no longer carried over by the with-meta payload above, because `automerge:ok` is now itself one of the 10 excluded markers.

   If splitting, create the child tickets using a **two-pass approach**:

   Split tickets must also receive the "Refined" label/tag since they were refined during this session — `/cenci:implement` checks for it as a pre-flight condition.

   #### Coverage gate — verify the split's acceptance-criteria partition

   Before creating any child, verify the proposal partitions the parent's acceptance criteria per `agents/refiner.md`'s **Acceptance-criteria partition** rule: every `- [ ]` item in the proposal's `### Acceptance Criteria` must appear in exactly one child's `### Acceptance criteria` checklist (scoped rewording is fine as long as the mapping is evident), and no criterion may appear under two children. If any criterion is unassigned or duplicated, STOP — create no tickets, run no Pass 2 — and report the violating criteria to the user so the split can be corrected in another refinement round. Unlike the write-failure protocol governing the writes below, nothing has been written at this point, so stopping here is free; proceeding would mint children whose closure can no longer prove the parent's criteria (#661).

   #### Pass 1: Create children with numbered titles and dependency info

   Create children **in dependency order** — independent children first, then children that depend on them (so you have their issue numbers for `Depends on` references).

   Each child title gets a `(K/N)` suffix, e.g. "Add API validation (1/3)".

   Each child body includes:
   - `Related to #<parent>` (links back to parent)
   - `Depends on #<sibling>` lines for any children it depends on (if applicable)
   - `Parallel with #<sibling>` lines for children it can run alongside (if applicable)
   - Its own `### Acceptance Criteria` section — the criteria the proposal's split assigned to this child (its slice of the parent's partition, verified by the coverage gate above); omit the section only for a child the split assigned zero criteria (e.g. a design-only first child)

   Capture each created issue number from the command output.

   Use the `Write` tool to create the raw title and body as plain text — `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt` (`<ticket-title> (K/N)`) and `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md` (`Related to #<original-number>\nDepends on #<sibling-number>\nParallel with #<sibling-number>\n\n<ticket-body>\n\n### Acceptance Criteria\n- [ ] <each criterion the split assigned to this child>`) — never a hand-escaped JSON literal; the title is free text and must never be interpolated directly into the command line. `<ticket-body>` here is that child's **full block** from the proposal's `### Suggested Split` — its `### Goal`, `### Decisions`, and `### Assumptions (auto-adopted)` subsections — so the created ticket is plannable without consulting the parent (AC 5); its own `### Acceptance criteria` and `### Dependencies` subsections are not repeated verbatim here, since the checklist appended by this template (sourced from the coverage-gate-verified partition) and the `Depends on #<sibling-number>` / `Parallel with #<sibling-number>` lines above already cover that content. Build the payload per the `shell-rules` skill's canonical `jq -n --rawfile` snippet, in whichever of the two forms the presence gate above selected. The parent's label names are externally sourced, so they enter the payload **only** via `--slurpfile` from the fetched metadata file — never interpolated into the command line, and `jq` cannot let slurped content influence the payload's structure.

   **With-meta** (the parent-metadata fetch above succeeded):
   ```bash
   jq -n --rawfile title ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt --rawfile body ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md --slurpfile meta ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json '{title: ($title | rtrimstr("\n")), body: $body, labels: (["Refined"] + [$meta[0].labels[].name | select(. as $n | (["Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"] | index($n)) | not)])} + (if ($meta[0].milestone.number // "") != "" then {milestone: $meta[0].milestone.number} else {} end)' > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K.json
   ```

   **No-meta** (the fetch still failed after its one retry — the graceful degrade above; `labels` is `["Refined"]` only, no `milestone` key):
   ```bash
   jq -n --rawfile title ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt --rawfile body ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md '{title: ($title | rtrimstr("\n")), body: $body, labels: ["Refined"]}' > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K.json
   ```

   **Ordered append for earned labels.** Both forms above show the **nothing-earned** case verbatim — `(["Refined"] + […])` with-meta, `labels: ["Refined"]` no-meta. When the Confirmation Gate computed (step 5 above) that this child earns any of `automerge:ok`, `Browser`, or `ui:visual-check`, extend the leading array literal in whichever form ran by appending each earned label — in that fixed order for each label the child earned at the gate — onto `"Refined"` before running `jq`; never reorder, and never append a label the gate did not compute for this child. Worked max-case example (a non-design child earning all three, with-meta form): the `labels` value becomes `(["Refined","automerge:ok","Browser","ui:visual-check"] + [$meta[0].labels[].name | select(...)])` — the `select(...)` exclusion clause is unchanged from the base form above.

   Then, whichever form ran:
   ```bash
   gh api repos/<owner>/<repo>/issues -X POST --input ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K.json --jq .number
   ```
   The `--jq .number` output *is* the new child's issue number — this confirms the API accepted valid JSON, but not that the title text itself is correct (a JSON-escaping mistake can mangle a title while still parsing).

   **Verify the title and label set persisted correctly** by re-fetching the new child and comparing the title against the intended `<ticket-title> (K/N)` and the labels against the gate-computed set for this child — including the **absence** of `automerge:ok`/`Browser`/`ui:visual-check` when the gate did not earn them for this child (a command exiting 0 is not proof they applied correctly — see the write-failure protocol above):
   ```bash
   gh issue view <child-number> --repo <owner>/<repo> --json title,labels --jq '.title, (.labels[].name)'
   ```
   If the title does not match exactly, or the label set includes a label the gate did not earn for this child or is missing one it did earn, follow the write-failure protocol: report the mismatch, retry the create once (fresh `Write` + `gh api repos/...` re-invocation), then re-verify; if still failing, STOP — do not link this child as a sub-issue, do not create any further children, and do not run Pass 2.

   **Link the child as a native sub-issue of the parent.** Immediately after parsing a child's number (children are created in dependency order, so link each one right after it is created — parent == `<original-number>`):
   ```bash
   gh issue edit <child-number> --repo <owner>/<repo> --parent <original-number>
   ```
   **Verify from the parent side** — re-fetch the parent's sub-issue list and confirm this child's number is present (idempotent: treat an "already linked" result, i.e. the child already appearing in the list, as success):
   ```bash
   gh issue view <original-number> --repo <owner>/<repo> --json subIssues --jq '.subIssues.nodes[].number'
   ```
   If the link or its verification fails, follow the write-failure protocol: report the error, retry the `--parent` edit once, then verify again; if still failing, STOP — do not create any further children, and do not run Pass 2. Report the partial state: which children exist and which are linked as sub-issues.

   If creation fails for a child, follow the write-failure protocol: report the error, retry once, and if still failing, STOP — do not create any further children, and do not run Pass 2. Report the partial state to the user: which children (if any) were already created, with their issue numbers.

   Omit `Depends on` / `Parallel with` lines that don't apply (e.g. the first child typically has no dependencies).

   Design-only children (see **Design-first splits** above) additionally carry `"Design"` in that child's JSON `labels` array — the seed array becomes `["Refined","Design"]` in whichever form ran (with-meta: `(["Refined","Design"] + [ …carried-over parent labels… ])`; no-meta: `labels: ["Refined","Design"]`) — and their body includes the `### Design Direction` section from this refinement (that's where `/cenci:design` reads it from). A design-only first child is a real child of the parent → link it as a sub-issue exactly like the others.

   #### Pass 2: Final sub-issue verification and (if ordered) an Execution Order note

   The native sub-issue list now renders the child enumeration and progress in the GitHub UI, so **do NOT append a child-ticket markdown checklist** to the parent body — there is no per-child checkbox list to write back.

   **(a) Final verification.** After all children are created and linked in Pass 1, re-fetch the parent's sub-issue list one last time and confirm **every** child number appears in `subIssues.nodes`:
   ```bash
   gh issue view <original-number> --repo <owner>/<repo> --json subIssues,subIssuesSummary --jq '.subIssues.nodes[].number, .subIssuesSummary'
   ```
   If any child is missing from the list, follow the write-failure protocol: report which child is not linked, retry that child's `gh issue edit <child> --parent <original-number>` once, then verify again; if still failing, STOP and report that children #`<c1>`, #`<c2>`, … exist but child #`<cN>` is not linked as a sub-issue of parent #`<original-number>`, so the user can link it manually with `gh issue edit <cN> --parent <original-number>`.

   **(b) Execution Order (only when the split has real ordering).** If any child `Depends on` another (i.e. the children are not all independent), append a concise **prose** `### Execution Order` section to the parent body — never a `- [ ]` checklist. Use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<original-number>-<token>.md` with the following content (this uses `<original-number>` — parent == original — with the SAME run token from step 10, not a new one):
   ```
   <existing-body>

   ### Execution Order
   #10 first → then #11 and #12 in parallel
   ```
   Then run:
   ```bash
   gh issue edit <original-number> --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/issue-<original-number>-<token>.md
   ```
   **Verify** by re-fetching the parent and confirming the `### Execution Order` section is present in the body:
   ```bash
   gh issue view <original-number> --repo <owner>/<repo> --json body --jq '.body'
   ```
   If the split has no ordering (all children independent), skip (b) entirely — the sub-issue list alone conveys the enumeration.

   If the Execution Order update or its verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that children #`<c1>`, #`<c2>`, … are linked as sub-issues of parent #`<original-number>` but the `### Execution Order` note did not persist, so the user can append it manually.

   #### Companion design ticket (frontend tickets, no split)

   If `designNeeded` is true, the ticket is **not** being split, and `isDesignTicket` is false, create a dedicated design ticket — design never runs on the implementation ticket itself:

   Use the `Write` tool to create the raw title and body as plain text — `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-title.txt` (`Design: <feature title>`) and `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-body.md` (`Related to #<number>\nBlocks #<number>\n\n### Goal\nProduce the design spec (`.pen` + `DESIGN.md`) for #<number> via `/cenci:design`.\n\n### Design Direction\n<the Design Direction section from this refinement>`) — never a hand-escaped JSON literal; the title is free text and must never be interpolated directly into the command line. Build the payload per the `shell-rules` skill's canonical `jq -n --rawfile` snippet, in whichever of the two forms the presence gate in **Parent metadata for inherited labels/milestone** selected — the companion design ticket inherits exactly like a split child, with `["Refined","Design"]` as its seed array, and — same 10-entry exclusion set as Pass 1 above — never `automerge:ok`, `Browser`, or `ui:visual-check`: a design ticket never receives `automerge:ok` (it always fails the `NOT isDesignTicket` override), and never receives `Browser`/`ui:visual-check` either (steps 2 and 12's design skip apply here too):

   **With-meta** (the parent-metadata fetch succeeded):
   ```bash
   jq -n --rawfile title ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-title.txt --rawfile body ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-body.md --slurpfile meta ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json '{title: ($title | rtrimstr("\n")), body: $body, labels: (["Refined","Design"] + [$meta[0].labels[].name | select(. as $n | (["Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"] | index($n)) | not)])} + (if ($meta[0].milestone.number // "") != "" then {milestone: $meta[0].milestone.number} else {} end)' > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design.json
   ```

   **No-meta** (the fetch still failed after its one retry — the graceful degrade above; `labels` is `["Refined","Design"]` only, no `milestone` key):
   ```bash
   jq -n --rawfile title ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-title.txt --rawfile body ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-body.md '{title: ($title | rtrimstr("\n")), body: $body, labels: ["Refined","Design"]}' > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design.json
   ```
   Then, whichever form ran:
   ```bash
   gh api repos/<owner>/<repo>/issues -X POST --input ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design.json --jq .number
   ```

   The `--jq .number` output *is* the new design ticket's issue number `<D>` — this confirms the API accepted valid JSON, but not that the title text itself is correct. **Verify the title and label set persisted correctly** by re-fetching and comparing the title against the intended `Design: <feature title>` and the labels against the exact set `["Refined","Design"]` plus any inherited non-excluded parent labels — including the **absence** of `automerge:ok`, `Browser`, and `ui:visual-check`:
   ```bash
   gh issue view <D> --repo <owner>/<repo> --json title,labels --jq '.title, (.labels[].name)'
   ```

   If creation fails, or the numeric issue number is returned but the re-fetched title does not exactly match or the label set does not exactly match, follow the write-failure protocol: report the error (or mismatch), retry once, and if still failing, STOP and report that the design ticket was not created (or was created with corrupted content — note `<D>` for manual cleanup), so the implementation ticket's body cannot be updated with a dependency line.

   No URL parsing is needed for `<D>`. Append a dependency line to the implementation ticket's body. Use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.md` (reusing the bare `issue-<number>-<token>.md` path from step 10, not a `-design` suffixed one) with the implementation ticket's current body plus an appended:

   ```
   Depends on #<D> (design)
   ```

   Then run:
   ```bash
   gh issue edit <number> --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.md
   ```

   **Verify** by re-fetching the implementation ticket and confirming the `Depends on #<D> (design)` line is present in the body:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json body --jq '.body'
   ```

   If the update or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that the implementation ticket #`<number>` was not updated with the `Depends on #<D> (design)` line — but design ticket #`<D>` exists, so the user can add the dependency line manually.

   When `/cenci:design <D>` completes, it closes #<D> and propagates the `Designed` label to this ticket, satisfying implement's Design gate.

11. **Add the "Refined" label and remove "Working":**

   **Use the effective `automerge:ok` grant for the parent computed at the `## Confirmation Gate` above (step 4) — do not recompute it here.** That computation already ANDed the refiner's `### Automation` verdict for the parent with the three skill-local safety overrides (`isDesignTicket` from step 2, `browserRequired` from step 8, and the **visual-check signals** match from the `frontend-classification` reference skill — the same signal set step 12 below writes) and applied the fail-closed default (an absent/empty `### Automation` entry, or any value other than exactly `grant`, is `withhold`). Steps 11-12 consume that already-computed value; they never re-derive it.

   The `automerge:ok` label was already ensured to exist before Pass 1 above.

   - If `isDesignTicket` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Design" --remove-label "Working"`
   - Else if `browserRequired` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Browser" --remove-label "Working"`
   - Otherwise:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --remove-label "Working"`
   - If re-refining and `browserRequired` is false but the issue currently has the `Browser` label, also add `--remove-label "Browser"`
   - If re-refining and `isDesignTicket` is false but the issue currently has the `Design` label, also add `--remove-label "Design"`
   - Append to whichever branch's command ran, orthogonally to the labels above: if the effective grant holds, append `--add-label "automerge:ok"`; otherwise, if the issue currently carries `automerge:ok` (re-refine withdrawing a prior grant), append `--remove-label "automerge:ok"`; otherwise append nothing.

   **Verify** by re-fetching the issue's labels and confirming the expected set — including `automerge:ok`'s presence or absence — is present/absent:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json labels --jq '.labels[].name'
   ```

   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report which labels did and didn't apply on ticket #`<number>`.

12. **Write `ui:visual-check` for the parent** (skip if `isDesignTicket` is true): **use the parent's visual-check-signals-match result computed at the Confirmation Gate above (step 4) — evaluation already happened there; this step only performs the write.** If it matched, add the label:
   `gh issue edit <number> --repo <owner>/<repo> --add-label "ui:visual-check"`

   **Verify** by re-fetching the issue's labels and confirming `ui:visual-check` is present:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json labels --jq '.labels[].name'
   ```

   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that `ui:visual-check` did not apply to ticket #`<number>`.

   This label signals to the implement skill that interactive browser verification via Playwright CLI should be used.

   **Mark this run complete.** Once step 12 has taken its action or been correctly skipped (`isDesignTicket` is true), and the write-failure protocol has not STOPped anywhere in steps 10-12, use the `Write` tool to create an empty file at `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok`. This marker is what step 13 checks before deleting anything. Confirm the write succeeded by re-reading it with `cat ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok` — if that `cat` errors, treat the marker write itself as failed (report it as such, distinct from a steps-10-12 failure) rather than letting step 13 silently read it as "absent" for an unrelated reason.

13. **Clean up this run's scoped temp files.**

   This step is reached from two distinct places: **the declined branch** (step 8 of the Confirmation Gate above, when the human chose Decline — steps 10-12 never ran at all) and **the normal post-write path** (after steps 10-12 ran, whether they all succeeded or one of them STOPped per the write-failure protocol). Handle the declined branch first, separately — it is not the same case as "marker absent" below even though both leave the `.ok` marker unwritten.

   **Declined branch.** Reached only from the gate's Decline option. The `.ok` marker is **intentionally absent** here — steps 10-12 never ran, so there is nothing to verify and no write to prove — which is different from the marker-absent case below, where steps 10-12 *did* run and one of them failed. Delete only the file this run wrote before the gate (the bundle; nothing else exists yet at decline time):
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md
   ```
   Report plainly that no ticket, label, or sub-issue mutation occurred, that `Working` and the assignee claim remain (per the gate's step 8), and that re-running `/cenci:refine <number>` is how to adjust the proposal. Skip the rest of this step — do not check the `.ok` marker file, it was never expected to exist.

   For a Confirm run, check whether this run completed successfully by attempting to read the marker file:
   ```bash
   cat ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok
   ```
   If the command errors (non-zero exit, e.g. `No such file or directory`), the marker is absent. If it exits 0 (silently, since the marker is an empty file), the marker is present.

   **If the marker is present** — every write in steps 10-12 succeeded and was verified, so it's safe to delete this run's temp files by explicit path (never a glob — an unmatched glob errors under some shells, and `rm -f` already ignores paths that don't exist, so listing files a given run didn't create — e.g. child/design files when there was no split or companion design ticket — is harmless):
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit.json ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-title.txt ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-body.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design.json ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-title.txt ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-design-body.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K.json ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok
   ```
   Repeat the child-K paths (with the actual `K` value substituted) for each child created in Pass 1 this run.

   **If the marker is absent (and this is not the declined branch above)** — an earlier step in 10-12 did not complete successfully (the write-failure protocol already STOPped before reaching this step). Skip cleanup entirely and state explicitly to the user that cleanup was skipped for this reason, preserving the run's `<token>`-scoped temp files for manual recovery.

### Final Message

The complete adopted proposal was already rendered once, at the `## Confirmation Gate` above, before any write — do not re-print it here. After steps 10-13 complete, present only a persistence notice plus a per-ticket verdict summary confirming that what was applied matches what was confirmed:

> Ticket #`<n>` updated. Labels: Refined[, Design][, Browser][, ui:visual-check][, automerge:ok]. [Created `N` child tickets: #`<c1>` (automerge: grant|withhold)[, #`<c2>` (automerge: grant|withhold)…].] [Created companion design ticket #`<D>`.] [Note: milestone/label inheritance was skipped — the parent's metadata could not be fetched, so the created tickets carry only their seed labels and no milestone.]
>
> Every applied label and grant/withhold decision matches what you confirmed at the gate above.

Do not name or suggest the next command to run (`/cenci:implement`, `/cenci:design`, etc.) here — that's covered by the **After Refinement** section below.

## After Refinement

**STOP HERE.** Your job is done. Do not:
- Enter plan mode or propose an implementation plan
- Offer to run `/cenci:implement` or start implementation
- Suggest next steps beyond what's described above

The user will explicitly invoke `/cenci:implement` when they're ready to proceed — or `/cenci:design` for design-only tickets (labeled `Design`).
