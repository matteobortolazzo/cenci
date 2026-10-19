---
name: refine
description: "Refine a ticket interactively until it is ready for planning."
compatibility: Requires Claude Code AskUserQuestion and cenci project configuration.
argument-hint: <ticket-id> [additional context]
user-invocable: true
disable-model-invocation: true
model: sonnet
allowed-tools: Read, Write, Glob, Task, Bash(gh issue view:*), Bash(gh issue edit:*), Bash(gh label create:*), Bash(gh api user --jq:*), Bash(gh api repos/:*), Bash(git remote get-url:*), Bash(jq -n:*), Bash(jq -e:*), Bash(mktemp -u ${TMPDIR:-/tmp}/cenci/:*), Bash(cat ${TMPDIR:-/tmp}/cenci/:*), Bash(rm -f ${TMPDIR:-/tmp}/cenci/:*), Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh":*), Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/coverage-map.sh":*), AskUserQuestion
---

> **Client dispatch**: In Codex, read `codex-runtime` and `refine/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

## Context

Read `project-core` and resolve neutral configuration before continuing.

Use the config returned by `project-core`; if none exists, stop with its client-appropriate setup guidance.

**Resolve refinement autonomy.** Right after `project-core`, resolve `refinementAutonomy` exactly once: this skill reads `refinement.autonomy` from the config returned by `project-core`. Only the exact string `"lean"` means lean. An absent key, a missing or non-object `refinement` block, or any other value means `interactive`, with the shared question filter and interactive relay/confirmation. `planning.autonomy` and `planning.attended` are never consulted. When `refinement.autonomy` is present but is not exactly `"lean"` or `"interactive"`, print one notice line, `refinement.autonomy "<value>" unrecognized; running interactive`, and continue in interactive mode — the mode is still `interactive`. Steps 4 and 6 and the `## Confirmation Gate` read that one value and never re-read the config.

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
gh issue view <number> --repo <owner>/<repo> --json number,title,body,labels,state,assignees,milestone,comments,author
```
**Split-child provenance detection:** Determine whether this ticket is itself a child of an earlier split — mirrors `agents/context-gatherer.md`'s parent-child detection. Primary source is the native sub-issue link:
```bash
gh issue view <number> --repo <owner>/<repo> --json parent --jq '.parent.number // empty'
```
If that returns a number, record `isSplitChild: true` and `parentNumber: <that number>`. **Fallback** (older tickets linked only by convention, or the primary command failing non-zero): if the first non-empty line of the fetched body matches `Related to #<number>`, record `isSplitChild: true` with that number as `parentNumber`. If neither yields a parent, record `isSplitChild: false`. A split child is presumed sized by its parent's refinement — split depth is one (`docs/ticket-sizing.md`) — so this flag drives the bundle's resolved flags (step 4), the **Split-depth guard** in step 9, and the **Oversize split child escalation** at the `## Confirmation Gate`.

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

## Ownership Inspection (read-only)

Read the `ticket-ownership` reference skill's `## Inspect ownership (read-only)`
section only, and follow it using the assignees from the ticket fetch above — this
performs zero GitHub writes. Record the result as `ownershipState`
(`unowned` | `owned-by-caller` | `conflict`) for the `## Confirmation Gate`
manifest below. The claim itself is deferred to the write phase (`## Update Ticket`
below, `#### Ownership claim (first write)`) — it must not run before the gate's
confirmation. Never replace an existing assignee.

## Your Role

You orchestrate backlog refinement. The judgment-heavy analysis — ambiguity hunting, question drafting, sizing, and the refined ticket proposal — is delegated to the **refiner** agent (spawned via the `Task` tool), whose `model: opus` frontmatter pin holds for its entire run; a skill-level pin only lasts the invoking turn, so it would silently degrade every follow-up turn of the Q&A loop to the session model. You run the interaction and the writes: relay the refiner's questions to the user, feed answers back, and perform every GitHub mutation yourself, all of them after the `## Confirmation Gate`. Never perform the refiner's analysis inline, and never delegate a GitHub write or an `AskUserQuestion` to a subagent (see the `subagent-safety` skill).

## Process

1. **Summarize** the fetched ticket for the user in 2-3 lines (title, current scope, state). Do not analyze it — that is the refiner's job.

2. **Classify ticket type**: Read the `frontend-classification` reference skill and apply its rule to determine if this ticket involves frontend/UI work. Record the result as `isFrontend` for the bundle and the labeling steps.

3. **Create the per-run temp-file token.** Run `mktemp -u ${TMPDIR:-/tmp}/cenci/issue-<number>-XXXXXX` once and capture the trailing random segment as `<token>` (the token is the random suffix only, e.g. `a1b2c3` — not the full mktemp basename). As with `<ticket-id-or-slug>` in the implement phases, carry the literal `<token>` value forward as text into every temp-file path for the rest of this run — do NOT re-derive it per Bash call, and do not use `$$`/shell state (it does not persist across separate Bash tool invocations). `-u` is a dry-run name generator — it only produces a unique-ish suffix, not an atomically-created file — which is why the `Write` tool is what actually creates each temp file in this run. The `<token>` is a **collision-avoidance mechanism only** — it reduces the chance of two concurrent runs picking the same temp-file basename — and is explicitly **not** an atomic reservation (a second run could theoretically generate the same suffix before either run's `Write` call lands) and **not** a security boundary (it provides no protection against a malicious or adversarial process targeting the same path).

4. **Write the context bundle.** Use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md` containing, in order:
   - The **verbatim** ticket title, body, labels, state, and comments from the fetch above — full text, never a digest or paraphrase (the refiner's decisions require source fidelity; see `docs/skill-authoring.md`).
   - Each attachment's summary alongside its downloaded file path (from the Attachments step).
   - The user context parsed from `$ARGUMENTS`, verbatim (or `None`).
   - The resolved flags: `isFrontend`, and `isSplitChild` with its `parentNumber` (from the **Split-child provenance detection** in the Context section above; `parentNumber` is omitted when `isSplitChild` is false), and `refinementAutonomy: lean | interactive` (the value resolved once in the Context section above).

   If the `Write` fails, retry once; if it still fails, STOP and report the error — the refiner must never run without the bundle.

5. **Delegate to the refiner agent** (`Task` tool, agent `refiner`). The first invocation's prompt contains only: the bundle path, a note that this is round 1, and the instruction to read the bundle file in full before analyzing. Later rounds additionally carry the Q&A history (step 6) — do not re-paste ticket or bundle content into any prompt; the bundle path is the context.

   Before relaying questions or adopting the proposal, apply the planning triggers in
   the plugin's `docs/model-escalation.md`: when triggered, delegate a bounded second
   opinion to `domain-specialist` and return validated corrections to the refiner.
   Preserve its output protocol and settled decisions; carry the specialist result
   across Q&A rounds. This pass does not add Q&A rounds or change `refinementAutonomy`.

6. **Q&A relay loop.** Apply the shared question selection in `agents/refiner.md` in both modes: resolve repo-grounded choices before asking, record them in the proposal, and allow zero questions. Parse the refiner's `## Questions` section.

   **Browser default before relay (both modes).** Inspect questions before either relay or lean-cap enforcement. When a question only asks whether ordinary interactive browser verification is wanted, and the resolved `isFrontend` or the step-8 browser signals already entail it, do not relay it. Respect an explicit user restriction on browser use; if it conflicts with the scope, resolve that material conflict under the shared policy instead of overriding it. Never filter security-sensitive, destructive, or unresolved product decisions, including production credentials, data exposure, irreversible browser actions, or unspecified UI behavior. Add each filtered question to the accumulated history with `A: Auto-adopted from classification — interactive browser verification is required (<classification rationale>); record in ### Assumptions (auto-adopted).` This is an agent-derived answer, never a user confirmation. Retain every other question and relay it under the normal mode rules below. Re-invoke the refiner with the bundle path and complete accumulated history after those answers arrive, or immediately if no questions remain. Filtered questions do not consume a slot; a correction-only pass does not consume a round, but any remaining questions actually relayed still count under the normal mode rules. Allow at most one correction re-invocation per run; if another already-entailed browser-only question returns, STOP with zero GitHub writes and report a refiner-policy violation instead of repeating the question to the user. The step-8 derivation and Confirmation Gate still determine parent/child labels and automerge independently.

   **Interactive relay — unchanged:** when `refinementAutonomy` is `interactive`, the three bullets below apply exactly as written, including their 4-question per-call and per-round cap and the rounds that continue until `None.`. The cap is a ceiling, never a target; further rounds require new material ambiguity from an answer or previously deferred material questions. Never discard an unanswered question just because the previous round reached its cap. When it is `lean`, the `None.` bullet still applies, the other two bullets are replaced by the lean relay sub-procedure that follows them, and the 4-question cap is replaced by the lean cap.
   - If it is `None.` → the same output contains the `## Refined Ticket Proposal`; continue to step 7.
   - Otherwise, present the round's questions to the user **in the refiner's order**. Present the round's questions in a single `AskUserQuestion` call — up to 4 questions per call, matching the refiner's per-round cap exactly. Never merge multiple refiner questions into one composite question, and never ask them as plain text. A round can exceed 4 only via the refiner's cap-exempt confirm/overrule question (`agents/refiner.md`'s entailed-decision rule); when it does, split into consecutive `AskUserQuestion` calls of at most 4 questions each, preserving the refiner's order across calls — never drop or defer the extra question. When the refiner supplied answer options for a question, map them onto the `AskUserQuestion` options using the refiner's wording (the user can always answer freeform via "Other"); otherwise offer sensible options. When the refiner marked one option recommended, map it onto the first `AskUserQuestion` option, appending `(Recommended)` to that option's label and carrying the rationale into its description; this is advisory: a question with no marked recommendation is relayed exactly as today and never triggers a re-invocation of the refiner.
   - When every question in the round is answered, re-invoke the refiner with the bundle path and the **complete** accumulated Q&A history — all rounds, as `Q:`/`A:` pairs using the refiner's original question wording; do not re-paste ticket or bundle content. Route the new output through this step again. The refiner asks at most 4 questions per round and rounds continue until it returns `None.`.

   **Lean relay sub-procedure** (`refinementAutonomy` is `lean` only). This skill enforces the lean cap itself and does not rely on the refiner to obey it. Lean mode allows one question round and at most 3 questions, plus the cap-exempt confirm/overrule question:
   1. **Partition and sort.** Split the round's questions into cap-exempt ones — tagged `(confirm/overrule — cap-exempt)` — and non-exempt ones, then stable-sort the non-exempt questions by class tag in this rank order: confirm/overrule, security, destructive, split, product ambiguity (the tags `(security)`, `(destructive)`, `(split)`, `(product)` map onto the last four ranks). An untagged question ranks last and counts against the cap, and its class is `product` for the over-cap suffix and ordering. Keep your own record of every question you withhold and the class tag you saw on it.
   2. **Relay.** Round 1 relays every cap-exempt question first, then at most 3 non-exempt questions in round 1 — the first 3 of the sorted order — using the same `AskUserQuestion` option mapping as the interactive bullets above and splitting into consecutive calls of at most 4 questions each, never dropping or deferring one. The cap-exempt question is always asked and never dropped, even when the 3-question cap is already full: it is outside the cap, so non-exempt questions can never squeeze it out.
   3. **Withhold the rest.** Withheld questions are never relayed to the user. Each non-exempt question beyond the 3rd goes back to the refiner as a `Q:`/`A:` pair using the refiner's original question wording and the fixed sentinel answer `Not asked — over the lean question cap; decide it under the overflow rule.` The refiner then decides it as a `decided: <answer> — <rationale> (alternative: <option label>) (over the cap: <class>)` entry.
   4. **Re-invoke and continue.** Re-invoke the refiner with the bundle path and the complete accumulated history (answered pairs and sentinel pairs alike); do not re-paste ticket or bundle content. From then on, after round 1 only cap-exempt questions are relayed. A cap-exempt question whose wording is identical to one already answered is not relayed again: the recorded answer is re-fed once, and if the refiner repeats the question after that, STOP with zero GitHub writes and report a lean-contract violation. At most 3 cap-exempt-only rounds follow round 1 in each pass (the Q&A loop is one pass, the overrule pass another); a further round is a lean-contract violation, so STOP with zero GitHub writes and report it.
   5. **Loop guard.** Non-exempt questions are fed back at most once: after round 1, again after the Confirmation Gate's overrule, and again after the `resplitAuthorized` re-invocation of the Oversize split child escalation, any non-exempt question still present in the refiner's output is a contract violation. When that happens, STOP with zero GitHub writes and report a lean-contract violation — the refiner returned a non-exempt question after the lean cap was enforced — and that re-running `/cenci:refine <number>` is how to retry. Never relay such a question.

   **Precedence.** Lean mode only reduces or reorders the refiner's questions; it never removes a confirmation, a STOP, or a fail-closed default (#979). The pre-flight prompts, attachment selection, the Oversize split child escalation, and the Confirmation Gate prompt are not refiner questions and sit outside the cap.

7. **Proposal received.** When the refiner returns `None.`, its output contains the `## Refined Ticket Proposal` — the summary content adopted in step 9 and persisted in steps 10-12.

8. **Derive `browserRequired` — never ask.** Set `browserRequired: true` when the ticket was classified frontend/UI in step 2, **or** the ticket/answers mention web scraping, browser automation, or manual browser testing; otherwise set `browserRequired: false`. Do not ask the user: UI work is verified in a browser, so the answer is entailed by the classification. The value is still visible and reviewable before any write — the `## Confirmation Gate` manifest shows the `Browser` label and the resulting `withhold` rationale — and a human who disagrees declines there and re-runs refine with context (e.g. "logic-only change, no browser needed") that keeps the frontend classification from firing. Lean mode does not change step 8: `browserRequired` is still derived from classification, never asked, and the `Browser` label is still applied from it.

9. **When refined**, adopt the refiner's `## Refined Ticket Proposal` **verbatim** as the summary content — its sections (`### Updated Title` (optional), `### Updated Description`, `### Acceptance Criteria`, `### Edge Cases`, `### Assumptions (auto-adopted)`, `### Decisions`, `### Technical Notes`, `### Design Direction`, `### Automation`, `### Size Estimate: <letter>`, `### Suggested Split` with `#### Parent items` and `#### Execution Order`) map 1:1 onto the persistence steps below; the section formats themselves are specified in `agents/refiner.md`. `### Assumptions (auto-adopted)` and `### Decisions` persist into the ticket body in step 10, same as `### Acceptance Criteria`; `### Automation` drives the `## Confirmation Gate`'s per-ticket `automerge:ok` verdict computation (parent and every child) and is never written into the body. Do not rewrite, summarize, or reorder the proposal's content. The proposal itself is rendered to the user at the `## Confirmation Gate` below, before any write — it is not held back until the final message; steps 10-13 persist the ticket update, any split, and the labels only after that gate's Confirm branch (see the **Final Message** note at the end of the Update Ticket section).

   A `### Suggested Split` in the proposal means each child becomes its own numbered ticket and PR, linked to the parent as a native GitHub sub-issue, with dependency ordering captured in the child bodies (Pass 1/Pass 2 below).

   **Split-depth guard (fail closed).** When `isSplitChild` is true and the adopted proposal nonetheless contains a `### Suggested Split`, STOP immediately unless `resplitAuthorized` was set earlier in this same run — by the human explicitly choosing the **Oversize split child escalation**'s third option below (`## Confirmation Gate`), and by no other means: `resplitAuthorized` can be set only that one way, in this run, for this child; it is never inferred, never carried over from a prior run, and never something the refiner sets on its own. **When the guard fires (`resplitAuthorized` not set):** render no Confirmation Gate manifest, ask no confirmation, perform zero GitHub writes — and report that the refiner violated the no-resplit contract for split child #`<number>` (child of #`<parentNumber>`; see `agents/refiner.md`'s **Split children are never split again** rule), and that re-running `/cenci:refine <number>` is how to retry. **When `resplitAuthorized` was set:** the proposal's `### Suggested Split` is expected — route it through the normal split path below (the Coverage gate, the Confirmation Gate's manifest render and single confirmation, Pass 1/Pass 2 child creation) exactly as any other split proposal, with no special-cased bypass; this creates grandchildren of the original parent, which is why the authorization must come from an explicit human decision made in this run, never an automatic one (see `docs/ticket-sizing.md`'s human-authorized exception). Split depth is one by default and grandchild tickets are never created automatically (`docs/ticket-sizing.md`); an L-sized split child with no resplit authorization is handled by the **Oversize split child escalation** at the `## Confirmation Gate` below, never by another split.

## Confirmation Gate

For a split, follow `parent-blockers.md` before rendering the manifest: fetch and validate the parent's native blockers, select only OPEN issues, and include that inherited set in every child's dependencies. Each child's effective blockers are the deduplicated union: **inherited open parent blockers + blocking siblings**. The first child can therefore have blockers even without a blocking sibling. Re-run the shared read-only revalidation before the first write; dependency drift requires fresh confirmation.

Placed here — between Process step 9 (the proposal is adopted) and `## Update Ticket` below — this gate performs **zero** GitHub writes; it only classifies, asks, computes, and renders. **No GitHub write of any kind occurs before** its single confirmation — the ownership claim, the `Working` label, and every ticket/label/sub-issue mutation all wait for it. Steps 10-13 below then consume everything this gate computes; they never recompute a verdict or a label set. A split proposal must also clear the Coverage gate precondition immediately below before this gate renders anything.

#### Acceptance-criteria concreteness check (every proposal)

Before anything else in this gate — split or not — read every `- [ ]` item under the adopted proposal's `### Acceptance Criteria` and `### Edge Cases`. Each must name an observable behavior, state or output: something a test asserts, a screenshot shows, or a doc line says. A criterion is **vague** — and the run STOPs — when it matches any of these shapes (case-insensitive): `works correctly`, `works as expected`, `works properly`, `handles edge cases`, `handles errors gracefully`, `is robust`, `is performant`, `is secure`, `behaves correctly`, or any item that names no concrete command, response, state, file or UI element at all. `### Edge Cases` must be present, with either one concrete `- [ ]` item per edge case or exactly `None.`; a missing section, or a "handles edge cases" line in place of individual items, is the same STOP. **When the check fails:** render no manifest, ask no confirmation, perform zero GitHub writes — report each rejected criterion verbatim with the reason (`vague: names no observable behavior, state or output`), and tell the user that another refinement round (re-run `/cenci:refine <number>`, optionally with context naming the observable to assert) is how to fix it. Nothing has been written at this point, so stopping here is free; proceeding would persist a ticket the acceptance reviewer can never grade.

#### Size Estimate check (every proposal)

Next, verify the proposal's `### Size Estimate: <letter>` section: the heading carries exactly one of `S`, `M`, `L` after the colon; the next line is `Bound: …` and matches that letter's bound line **verbatim** from `docs/ticket-sizing.md`; a `Rationale:` line follows with one or two sentences; and, for `L`, the bound line's `<reason>` placeholder is replaced by an actual reason why the slices cannot ship as a stacked series of M PRs. A missing section, a letter outside `S`/`M`/`L`, a bound line that does not match the letter, an `L` whose bound still reads `<reason>`, or a rationale that sizes up on model capability (any "the model can handle it" shape) is a STOP on the same zero-write terms as the concreteness check above: report the exact defect and that another refinement round is how to fix it. When the rationale states that the scope **exceeds L** and the proposal carries no `### Suggested Split` (and the ticket is not a split child), that is also a STOP — anything beyond L is split, never written as one ticket.

#### Coverage gate — verify the split's acceptance-criteria partition

Before rendering a manifest for a proposal carrying a `### Suggested Split`, run the structural completeness check first, the acceptance-criteria partition check second, the coverage-map check third — all within this same gate, all gating the same pre-render stop point.

**Structural completeness check.** For every child block in the adopted `### Suggested Split`, verify all six subsections are present: `### Goal`, `### Size`, `### Decisions`, `### Assumptions (auto-adopted)`, `### Acceptance criteria`, and `### Dependencies`. Each present subsection must also satisfy its emptiness rule: `### Goal` must contain non-empty prose — never a placeholder or blank section; `### Size` must contain a real `<S/M> — <reasoning>` value — never empty, and never the "None." sentinel (a split child is always S or M, never L, and always has a real size); `### Dependencies` must be non-empty ("None." is a valid value when the child truly has no dependencies); `### Decisions` and `### Assumptions (auto-adopted)` must each be non-empty or exactly "None."; `### Acceptance criteria` may be empty only for a child the partition assigned zero criteria. If any child is missing a subsection or violates its emptiness rule, STOP before any child creation — render no manifest, ask no confirmation, create no tickets, run no Pass 2 — and report the violating child's `(K/N)` title and the missing or empty section(s) so the split can be corrected in another refinement round. This check only confirms presence/absence and does not itself judge whether an empty `### Acceptance criteria` section is legitimate — the acceptance-criteria partition check below is the sole verifier of correct assignment, and a child wrongly left empty will surface there as an unassigned criterion.

**Acceptance-criteria partition check.** Only after the structural completeness check passes for every child, verify the proposal partitions the parent's items per `agents/refiner.md`'s **Acceptance-criteria partition** rule: the proposal's `#### Parent items` list enumerates every `- [ ]` item of `### Acceptance Criteria` and `### Edge Cases` plus every verification step, documentation item and boundary the ticket names (an item of the parent missing from the enumeration is itself a STOP — the enumeration is the contract, so a dropped item is a dropped requirement), and every numbered item must appear in exactly one child's `### Acceptance criteria` checklist, copied verbatim with its `(P<n>)` tag, and no item may appear under two children. Each child's `### Size` must be S or M unless its bound line records why that child is L. If any criterion is unassigned or duplicated, STOP — render no manifest, ask no confirmation, create no tickets, run no Pass 2 — and report the violating items to the user so the split can be corrected in another refinement round; the same STOP applies when a child sizes L without a recorded reason. Nothing has been written at this point, so stopping here is free; proceeding would mint children whose closure can no longer prove the parent's criteria (#661), or, per the structural check above, whose `Refined` label is not truthful (#872).

**Coverage-map check (mechanical).** The partition check above is your reading; this one is the script's — `scripts/coverage-map.sh`, the refine skill's own helper. Use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-items.md` with the proposal's `#### Parent items` list verbatim (one `<n>. [<kind>] <text>` line per item), and, for each child K, `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-criteria.md` with that child's full `### Acceptance criteria` checklist verbatim. Then run:
```bash
bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/coverage-map.sh" check --items ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-items.md --child "child 1/N=${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-1-criteria.md" --child "child 2/N=${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-2-criteria.md"
```
(one `--child` per proposed child). Exit 0 prints `items=<n> owned=<n> children=<N>` and the rendered map — carry those counts into the manifest row for the parent. **Exit 3 — an item has no owner: the split fails.** STOP on the same zero-write terms — report every `unowned:` line the script printed verbatim, state that the split is wrong (not that the item is optional), and that another refinement round is how to re-cut it; never drop the item and never proceed with a partial map. **Exit 4 — an item has two owners:** the same STOP, reporting the `duplicate:` lines. **Exit 2 — malformed enumeration or an unreadable file:** the same STOP, reporting the script's message; a malformed `#### Parent items` list is a refiner output defect, never something to hand-repair here.

**Oversize split child escalation (before rendering).** When `isSplitChild` is true, `resplitAuthorized` is not already set for this run, and the adopted proposal's `### Size Estimate` is L, surface the refiner's parent re-partition recommendation before rendering the manifest and ask, via `AskUserQuestion`:
   "This ticket is a split child of #`<parentNumber>` and still sizes L (budget risk). Split depth is one, so it will not be split again unless you explicitly authorize it here — proceed with it as-is, decline so parent #`<parentNumber>`'s partition can be redone, or explicitly authorize splitting this child anyway?"
   Options: "Proceed — keep the oversize child as-is" / "Decline — redo the parent's partition" / "Split this child anyway (human-authorized; creates grandchildren)". **Proceed** ⇒ continue into the numbered steps below with the proposal unchanged (the L reasoning stays in the persisted body's `### Technical Notes`-adjacent sections exactly as the refiner wrote it). **Decline** ⇒ zero GitHub writes have occurred and none will occur — jump to step 13's declined-cleanup branch, and report that re-running `/cenci:refine <parentNumber>` is how to re-partition the parent. **Split this child anyway** ⇒ zero GitHub writes have occurred yet: set `resplitAuthorized = true` for the remainder of this run. This is the only way `resplitAuthorized` is ever set — the refiner never sets it itself, and no other input, prior run, or invocation can set it. Then re-invoke the refiner agent (`Task` tool) with the bundle path, the complete accumulated Q&A history exactly as step 6's re-invocation does, **plus one new line in the prompt**: `resplitAuthorized: true` (the human operator explicitly authorized splitting this split child; evaluate whether a split is warranted and, if so, emit `### Suggested Split`). In lean mode, step 6's loop guard covers this re-invocation too: any non-exempt question in its output is a lean-contract violation, so STOP with zero GitHub writes and report a lean-contract violation, while a cap-exempt question is relayed through step 6's lean relay sub-procedure. Route the refiner's newly returned proposal back through Process step 9 and this Confirmation Gate again from the top — the Coverage gate's structural completeness and acceptance-criteria partition checks, a fresh manifest render, and the gate's single confirmation — exactly as any other split proposal; there is no special-cased bypass of any of those checks for an authorized resplit. The **Split-depth guard** above does not fire on this re-derived proposal, because `resplitAuthorized` is now set for this run. Either way but the third option, this escalation only asks — it never modifies the proposal itself, and Proceed/Decline never split: this is the human hand-off for an oversize child, replacing *automatic* grandchild creation — the third option is the sole *human-authorized* exception to that (see `docs/ticket-sizing.md`).

   **Why the `resplitAuthorized`-not-already-set clause prevents an infinite loop.** The refiner's re-derived proposal, per the Split-depth guard's own exception above, is expected to still carry `isSplitChild: true` and often still `### Size Estimate: L` — an authorized split is only interesting when the child really is oversized, so re-confirming L here is the normal case, not a bug. Without this clause, that re-derived L-on-a-split-child proposal would satisfy this escalation's trigger a second time and re-ask the identical 3-option question — and choosing the third option again would re-invoke the refiner and land right back here, looping indefinitely. With `resplitAuthorized` now set for this run, this escalation's trigger condition is no longer satisfied on the second pass, so it does not re-ask: the refiner's re-derived proposal instead falls straight through to the Coverage gate and this Confirmation Gate's normal manifest render and single confirmation below, exactly like any other split proposal reaching this gate for the first time.

1. **Per-child classification** — only when the adopted proposal carries a `### Suggested Split`. For each proposed child K, apply the `frontend-classification` reference skill to **that child's own block text** (its title, `### Goal`, and `### Acceptance criteria`) — never an inlined keyword list; that skill is the single source of truth. Record `childIsFrontend(K)` and `childVisualCheck(K)` (the visual-check signal subset) per child.

2. **Per-child browser derivation** — derive `childBrowserRequired(K)` from that child's own block text exactly as step 8 derives the parent's value, never by asking: `true` when `childIsFrontend(K)` is true or the block mentions web scraping, browser automation, or manual browser testing; otherwise `false`.

3. **The parent's step-8 value is independent of every child.** Step 8's `browserRequired` applies to the parent ticket only and **is never propagated to any child** — on a split the parent is a tracking epic, so a `browserRequired: true` parent value must not force `Browser` or a withhold onto a child, and a `false` parent value must not clear a child's own `true`.

4. **Compute effective verdicts**, per ticket T in {parent, child 1…N}:
   `effective grant(T) = ### Automation verdict for T is exactly "grant" AND NOT browserRequired AND NOT visual-check-signals-match` (each override evaluated for T itself — the parent's own `browserRequired`/visual-check match for the parent; for a child K, that child's own `childBrowserRequired(K)` and `childVisualCheck(K)`).
   **Fail-closed default preserved**: an absent/empty `### Automation` entry for T, or any value other than exactly `grant`, is `withhold`.

5. **Compute the label set per ticket**: the parent's set is computed per steps 11-12 below (unchanged in shape; steps 11-12 consume the gate-computed values rather than recomputing them); each child's set = inherited non-excluded parent labels + `Refined` [+ `Browser` when `childBrowserRequired(K)`] [+ `ui:visual-check` when `childVisualCheck(K)`] [+ `automerge:ok` when its effective grant holds]. **Record the gate-time parent snapshot** — the parent's current `labels` and `milestone`, as fetched for this gate — as the basis for "inherited non-excluded parent labels" above; this is a preview, not a promise. It is reconfirmed at write time by `## Update Ticket`'s **Manifest revalidation** sub-step (run before the first write), which re-fetches the parent and diffs it against this gate-time snapshot. That diff is split into two kinds: **authorization-sensitive drift** — a change in `automerge:ok`, `Browser`, or `ui:visual-check` on the parent between the gate and the write — versus **cosmetic drift** — everything else (milestone, `area:*`, priority, team, or any other label). The gate's own `automerge:ok`/`Browser`/`ui:visual-check` computation above is authoritative for what this run computed and does not itself change; it is the parent's *current remote state* that may have moved, which is exactly what the revalidation sub-step exists to catch.

6. **Render** the complete adopted proposal, followed by a manifest: one row per ticket to be updated or created, with its title, final label set, its milestone, its intended hierarchy/dependencies (`Related to` / `Depends on` / `Parallel with` lines and native sub-issue links), and `grant`/`withhold` plus a one-line rationale. Include a row for the parent's own pending mutations: the ownership claim (or `already owned by you` when `ownershipState` is `owned-by-caller`) and the `Working` label transition. If the parent already carries sub-issues from a prior run, state in the manifest that those existing children will not be modified — and note that a child created before this gate existed may still carry a legacy `automerge:ok`/`Browser`/`ui:visual-check` grant inherited under the old behavior; this run does not audit or revoke it, since the unmodified child is untouched by design.

   **Lean mode — the A-list.** When `refinementAutonomy` is `lean`, the render also numbers every `### Assumptions (auto-adopted)` item and every `decided:` entry continuously as `A1…An`, in one list the human can reference by number. The numbering is display-only; the persisted `### Assumptions (auto-adopted)` keeps plain `-` bullets. The list opens with a group headed `Decided over the cap — review before confirming`, which lists the entries decided over the cap first, under that heading, with security and destructive entries first; an entry is over-cap when it ends in the `(over the cap: <class>)` suffix, and its `<class>` orders it. This skill orders and reconciles that group from its own record of the questions it withheld and the class tags it saw (an untagged question's class is `product`), not only from the suffix on the refiner's re-emitted entries: it matches each withheld question to its over-cap entry, takes the class from its own record when the suffix is missing or disagrees, and reconciles after every refiner output, including the overrule output. The remaining `decided:` entries and the assumptions follow, numbering continuing. Whenever a withheld question has no matching over-cap entry, list it under the same heading, marked "no recorded decision", so nothing withheld is invisible at Confirm. In interactive mode this render is unchanged and carries no A-list.

7. **One confirmation.** Ask, via `AskUserQuestion`:
   "Apply this refinement as shown?"
   Options: "Confirm — apply as shown" / "Decline — make no changes". In interactive mode the gate is unchanged. No adjust loop — a decline requires a fresh `/cenci:refine <id>` run to change anything.

   **Lean mode — one overrule.** When `refinementAutonomy` is `lean`, the human may answer this question with a freeform "Other" answer, which is an overrule rather than a decline. The overrule is available once per run. A freeform answer is always an overrule, even when its text reads like "no" or "decline". Empty or whitespace-only "Other" text is treated as Decline. This skill records the overrule as `Q: Confirmation Gate overrule` / `A: <text>` in the Q&A history and re-invokes the refiner exactly once (`Task` tool) with the bundle path and the complete accumulated history. Per `agents/refiner.md`'s `## Lean Question Policy`, the refiner may then ask only cap-exempt questions before one final proposal. This is one overrule pass, never a second overrule: cap-exempt questions the refiner raises during it are relayed through step 6's lean relay sub-procedure and answered before the single final proposal, and the resumed refiner invocations that carry those answers are part of the same pass. During the overrule pass, a failed `Task`, an output with neither a proposal nor questions, a malformed proposal, or any non-exempt question is a lean-contract violation: STOP with zero GitHub writes and report a lean-contract violation. While it is pending the overrule itself performs no GitHub write: the new proposal re-routes through Process step 9 and this Confirmation Gate from the top — the Split-depth guard, the Coverage gate, the Oversize split child escalation, and steps 1-6 above, with a fresh gate-time snapshot — so every verdict and label set is recomputed from it. The re-rendered gate offers only Confirm and Decline. Any answer other than Confirm counts as Decline there, including freeform "Other" text, and a decline follows step 8's zero-write path. The overrule never replaces the single confirmation: nothing is written before a Confirm. Lean mode never grants automerge by itself — `### Automation` verdicts and step 4's effective-grant computation are identical in both modes.

8. **Decline** ⇒ zero GitHub writes have occurred and none will occur — no ownership claim, no `Working` label, no ticket/label/sub-issue mutation of any kind — so no cleanup mutation is needed: title, body, labels, assignees, milestone, and native sub-issues are state-for-state exactly as they were when this run started. Jump straight to step 13's declined-cleanup branch below, and report that re-running `/cenci:refine <id>` is how to adjust the proposal.

**Confirm** ⇒ continue into `## Update Ticket` below, where steps 10-13 apply every effective verdict and label set exactly as rendered in the manifest.

## Update Ticket

> **CRITICAL**: This section is mandatory after refinement. Do NOT skip it.
> **No GitHub write of any kind occurs before** the human confirms at the `## Confirmation Gate` above — that gate, not the Q&A loop (steps 6-8), is the pre-write authorization boundary. The Q&A loop only shapes the proposal's content; steps 10-13 below execute only after the gate's Confirm branch, and a Decline stops before any of them run (see step 13's declined branch).

> **Write-failure protocol**: Every *edit* in this section (ticket body/title updates, parent tracking updates, label add/remove) MUST be verified by re-fetching the resource with `gh issue view ... --json ...` and confirming the expected change is actually present — a command exiting 0 is not sufficient proof. Ticket *creation* is the one exception: `--jq .number` on the `gh api repos/...` response returns the new issue number directly — a numeric value is the proof; empty output, non-numeric output, or a non-zero exit is a failed create, so no separate re-fetch is required there. A malformed JSON payload surfaces as an API 4xx parse error and is itself a failed write — handle it with the single documented retry below, not a hand-patch loop. This retry also covers the local `Write` tool call that authors the raw title/body files and the `jq -n --rawfile` invocation that composes the JSON payload from them (see the `shell-rules` skill's canonical snippet): if a raw-file `Write` call fails, or the subsequent `jq` invocation exits non-zero, or the payload file is missing/empty/stale when the `gh api repos/... --input` command is about to read it, retry the failed step (`Write` or `jq`) once before (re-)invoking that `gh api repos/...` command — do not assume a local Write or jq failure is instead an API-side rejection. If the write or the verification fails:
> 1. Report the error to the user.
> 2. Retry the write once, then verify again.
> 3. If it still fails, **STOP** — do not proceed to the next step — and emit a partial-state report: what succeeded so far (with concrete issue/label numbers or names), what failed, and what the user needs to do manually to reconcile it. Each write point below states what belongs in that report.

**Per-run temp-file token**: The `<token>` used in every temp-file path of this section is the one created in Process step 3 — carry that same literal value forward; never mint a second token mid-run (two tokens in one run would orphan the earlier files from step 13's cleanup list).

### Write order

The canonical, machine-verified order of every GitHub write this skill performs, reached only via the gate's Confirm branch — nothing before this point ever writes:

1. `claim` — the ownership claim, first write (`#### Ownership claim (first write)` below).
2. `working` — the `Working` label ensure + add (`#### Label "Working"` below).
3. `parent-body` — the parent ticket's title/body update (step 10).
4. `child-create:1` immediately followed by its own `child-link:1`, then `child-create:2` immediately followed by its own `child-link:2`, immediately followed by its own `child-blockers:2`, and so on for every split child in dependency order (Pass 1) — every child's create+link(+blockers) group completes before the next step. This canonical illustration assumes no open parent blockers and child 2 depending on child 1. The blockers op is optional only when the child's effective blocker set is empty; with inherited open blockers, even the first child runs its blockers op immediately after its own link. (No additional backtick-quoted op token here: this section is machine-extracted as one illustrative sequence.)
5. `parent-exec-order` — Pass 2's parent body append: the `### Coverage map` on every split, plus the Execution Order note when the split has real ordering (step 10 Pass 2(c), after the read-only self-check in 2(b)); unused when there is no split.
6. `refined` — step 11's `Refined` label add / `Working` label removal.
7. `visual-check` — step 12's `ui:visual-check` label add.

#### Manifest revalidation (read-only — before the first write)

For a split, repeat `parent-blockers.md`'s fetch, validation, and open-set comparison now. Failure or dependency drift stops with zero writes. Retain the validated inherited set for the child body and native-link steps below.

Reached immediately after the gate's Confirm, before the ownership claim, before `Working`, before any other write. Fetch the parent's current milestone and labels **to a file**, as its own Bash call (a fetch failure then surfaces distinctly, before any write), mirroring the same pattern in `implement/phases/phase-9-pr.md`. This fetch is now **unconditional** — it runs on every Confirm, whether or not this run creates split children, because it also guards the parent's own label write (steps 11-12) against write-time drift, not only child inheritance. Do not reuse the `labels`/`milestone` already in context from the Context section's fetch or from the gate's own snapshot: both are stale by the time this runs, and externally-sourced label names must never be interpolated into a command line (`docs/skill-authoring.md`) — the file is consumed mechanically by `jq --slurpfile` below, never read back into shell text. The trailing `|| rm -f` is load-bearing: it removes a partially-written file so the presence gate below can never mistake one for a good fetch.
```bash
gh issue view <number> --repo <owner>/<repo> --json milestone,labels > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json || rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json
```

**Presence gate — fail closed, zero writes (D1).** Read the file back, then validate its content is actually usable — an exit-0 `cat` alone is not proof the fetch produced real data:
```bash
cat ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json
```
```bash
jq -e 'has("labels")' ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json > /dev/null
```
If the `cat` command errors (non-zero exit, e.g. `No such file or directory`), OR the file exists but the `jq -e 'has("labels")'` check fails (empty file, malformed JSON, or valid JSON missing the expected `labels` shape) — treat a present-but-empty-or-malformed file exactly the same as a missing one, never as a good fetch — retry the fetch once. If it errors again by either check — the parent cannot be read after one retry — **STOP**: zero writes have occurred anywhere in this run, so stopping here is free (D1; converges with the fail-closed precedent for the identical fetch at `implement/phases/phase-9-pr.md:263` — that fetch's graceful-degrade rationale no longer applies here now that this fetch precedes every write instead of following one). Report that ticket #`<number>`'s metadata could not be fetched and that re-running `/cenci:refine <number>` is how to retry. Do not claim ownership, do not add `Working`, do not update the parent body, do not create children.

**Drift classification — fail closed on authorization-sensitive drift (D2).** If the fetch succeeds, diff the freshly fetched `labels` against the gate-time parent snapshot recorded at step 5 of the `## Confirmation Gate` above:
- **Authorization-sensitive drift** — the parent's `automerge:ok`, `Browser`, or `ui:visual-check` labels changed since the gate rendered its manifest: **STOP** — zero writes anywhere in this run — stop and ask for a fresh confirmation: report exactly what changed and tell the user to re-run `/cenci:refine <number>` from scratch. No in-session re-gate: the gate's single confirmation authorizes exactly the manifest it rendered, and re-rendering here would violate "steps 10-13 never recompute a verdict or a label set."
- **Cosmetic drift** — any other label or milestone change (e.g. `area:*`, priority, team, milestone): proceed with the freshly fetched snapshot (not the gate-time one) for every inheritance decision below, and disclose the cosmetic label drift in the **Final Message**.
- **No drift**: proceed with the freshly fetched snapshot as normal.

#### Ownership claim (first write)

The first GitHub write of the run. Read the `ticket-ownership` reference skill's `## Claim ownership (mutating)` section and follow it — re-verify exclusive ownership as the first action after the gate's Confirm, using a fresh fetch: the `ownershipState` recorded in `## Ownership Inspection (read-only)` above may be stale by the time Confirm is reached, since time has passed for the Q&A loop and the gate's questions. If a conflict is found (a different assignee, or multiple assignees since the inspection), **STOP**: a conflict stops here with zero other writes — no `Working`, no parent body edit, no child creation — and report the observed state exactly as `ticket-ownership` specifies. Otherwise the claim proceeds and this run is now the ticket's sole assignee.

#### Label "Working"

Immediately after the ownership claim succeeds, add the "Working" label to signal that the ticket is actively being worked on. `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure it exists first — run each as its own Bash call (`|| true` swallows only the "already exists" error):
```bash
gh label create "Working" --repo <owner>/<repo> --color "FBCA04" --description "Actively being refined or implemented" 2>/dev/null || true
```
```bash
gh issue edit <number> --repo <owner>/<repo> --add-label "Working"
```
Apply the same ensure-then-add pattern to every label this skill applies later (`Refined`, `Browser`, `ui:visual-check`, `automerge:ok`, …): before the first `--add-label <name>` of a label, run its `gh label create <name> … || true` with the color/description from the lifecycle table in `/cenci:configure`.

**Ensure gate-decided labels exist, before Pass 1.** Step 10's Pass 1 below may seed `automerge:ok`, `Browser`, and/or `ui:visual-check` directly into a child's create payload, and step 11 may add `automerge:ok` to the parent — `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure all three exist now, before any child is created (the ensure-then-add rule already exists in `#### Label "Working"` above; this only relocates it ahead of the writes that need it, using the same colors/descriptions as the canonical table at `configure/SKILL.md:715`). Run each ensure as its own Bash call — idempotent (`|| true`), so running one this run doesn't need is harmless:
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

   `<updated description>` is not description-only: the body file's content is the concatenation of every ticket-body section the proposal carries — `### Updated Description`, `### Acceptance Criteria`, `### Edge Cases`, `### Assumptions (auto-adopted)`, `### Decisions`, `### Technical Notes`, `### Size Estimate: <letter>` (with its `Bound:` and `Rationale:` lines), plus `### Design Direction` when present — in that order; `### Automation` is never included, and neither is `### Suggested Split` itself (its children are persisted as their own separate tickets, never inlined into the parent body; the parent gains only the `### Coverage map` Pass 2 appends). On a split the parent's `### Acceptance Criteria` and `### Edge Cases` are written exactly as the proposal carries them — the children copy items, they never take them away from the parent.

   **Verify the update succeeded** — re-fetch the ticket and confirm the body (and, when retitled, the title) changed. Compare against a meaningfully wide slice of the body, not just its opening — mid-body corruption from an escaping mistake could otherwise land past a short truncation and go unnoticed:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json title,body --jq '.title, (.body[:2000])'
   ```

   If the update or verification failed, follow the write-failure protocol: report the error, retry once, and if still failing, STOP — do not create children, do not run Pass 2, do not proceed to steps 11-12 — and report to the user that ticket #`<number>`'s description/title update did not persist, so they can retry manually.

   #### Parent metadata for inherited labels/milestone

   Every ticket this skill creates — each split child — inherits the parent's milestone and its non-lifecycle labels, so a split never drops its children out of the milestone or loses the parent's `area:*`/priority/team labels. The fetch is unconditional now — it already ran, before the first write, in `#### Manifest revalidation (read-only — before the first write)` above, whose presence gate and drift classification already ran too. Reuse that same file here (`${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json`) — do not re-fetch. That file is consumed mechanically by `ensure-issue.sh init`'s own `--parent-meta`/`--slurpfile` merge (see **Creation Checkpoint** below), never read back into shell text.

   **What is inherited.** When given `--parent-meta <file>`, `ensure-issue.sh init` carries over every label the parent currently has except the 10 lifecycle/transient and per-ticket-grant markers — `"Refined","Working","Planned","In Review","Implemented","Design","Designed","automerge:ok","Browser","ui:visual-check"` — which are per-ticket state or per-ticket grants, never classification. `Design`/`Designed` remain in this exclusion list purely for legacy compat: a parent ticket refined before the design-stage removal may still carry one of those labels, and it must never be inherited onto a new child. The label this skill seeds (`Refined`) is applied on top regardless of what is carried over; each child's own earned `automerge:ok`/`Browser`/`ui:visual-check` (computed at the `## Confirmation Gate` above) are appended separately, per child, into that child's manifest entry below — never inherited from the parent's current labels. Two invariants carry over verbatim from phase-9's already-tested form, and are the non-obvious parts — both now enforced inside `ensure-issue.sh` rather than inline here:
   - the milestone must be the numeric `.milestone.number`, not the title — the REST endpoint requires the id;
   - the `milestone` key is resolved via an explicit jq emptiness check, never a bare `//` fallback that would emit `null` (see `docs/shell-scripting-gotchas.md`).

   **`automerge:ok`, `Browser`, and `ui:visual-check` are never inherited.** A split child and (per the followup-creation sites this same ticket also narrows) a followup ticket never inherit a parent's hands-off-merge grant or its browser/visual-check markers — each child's verdict and label set is instead applied explicitly from the gate, independently, into that child's manifest entry (steps 4-5 of the Confirmation Gate above). This closes the leak the old inheritance behavior had: a pre-existing `automerge:ok` already on the parent from a prior run is no longer carried over, because `automerge:ok` is now itself one of the 8 excluded markers.

   If splitting, create the child tickets using a **two-pass approach**:

   Split tickets must also receive the "Refined" label/tag since they were refined during this session — `/cenci:implement` checks for it as a pre-flight condition.

   The **Coverage gate — verify the split's acceptance-criteria partition** already ran, pre-render, as a precondition of the `## Confirmation Gate` above — a split that reaches this point already passed both the structural completeness check and the acceptance-criteria partition check, so Pass 1 below never re-verifies them.

   ## Creation Checkpoint

   Every child is created through `scripts/ensure-issue.sh` — a deterministic create/recover/repair/link helper, invoked identically here and in `codex.md` via its four subcommands, `ensure-issue.sh init`, `ensure-issue.sh ensure`, `ensure-issue.sh link`, and `ensure-issue.sh clear` — that makes creation recoverably idempotent across timeouts, retries, crashes, and a resumed `/cenci:refine <id>` session. It never performs a duplicate create: each manifest entry mints a nonce at `init` time and embeds a hidden `<!-- cenci-refine-create:<nonce> -->` marker in the created issue's body, so a resumed run recovers the same issue by re-scanning for that exact marker instead of re-POSTing blind.

   **Before the first POST**, initialize the run's checkpoint:
   ```bash
   bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh" init --repo <owner>/<repo> --parent <original-number> --checkpoint .plans/.refine-<original-number>.checkpoint.json --manifest ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-manifest.json
   ```
   Add `--parent-meta ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json` — the file fetched unconditionally, before the first write, in `#### Manifest revalidation (read-only — before the first write)` above; that fetch's presence gate already guarantees the file exists and is valid by the time this step runs, so `--parent-meta` is always passed here, never omitted. The checkpoint lives at `.plans/.refine-<original-number>.checkpoint.json` — keyed by the resource it recovers (this repo and this parent issue), not this run's `<token>`, so a crashed or interrupted run resumes correctly even under a fresh temp-file token on the next attempt (durable recovery state is keyed by the resource it recovers and lives in `.plans/`, never a run-scoped or `/tmp` bookkeeping file — see `docs/pipeline-safety.md`).

   Every later `ensure-issue.sh ensure`/`ensure-issue.sh link` call in Pass 1 reads this same checkpoint. If it is missing or corrupt (bad JSON, wrong `schemaVersion`), or a checkpoint field can't be read or fails validation, on any call other than `init`, the script itself exits 11 and does **fail closed** — it never silently re-POSTs. Treat that exit exactly like any other write-failure-protocol STOP: report the error and do not proceed.

   **Once this run completes successfully** (the `.ok` marker in step 12 below is written), clear the checkpoint:
   ```bash
   bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh" clear --checkpoint .plans/.refine-<original-number>.checkpoint.json
   ```
   `clear` is idempotent — calling it twice is not an error. A run that STOPped partway through steps 10-12 (per the write-failure protocol) **retains** the checkpoint instead: the next `/cenci:refine <original-number>` invocation resumes from it rather than re-creating already-created issues.

   #### Pass 1: Create children with numbered titles and dependency info

   Create children **in dependency order** — independent children first, then children that depend on them (so you have their issue numbers to mark as blockers).

   Each child title gets a `(K/N)` suffix, e.g. "Add API validation (1/3)".

   Each child body includes:
   - `Related to #<parent>` (links back to parent)
   - `Depends on #<blocker>` lines — one per effective blocker (inherited open parent blocker or blocking sibling, deduplicated), each on its own line, placed immediately after `Related to #<parent>` and before any `Parallel with #<sibling>` line. For siblings this is `Depends on #<sibling>`. These are a **permanent, human-visible supplement** to the native `--add-blocked-by` links applied after creation, never a replacement. A child with no effective blockers gets no `Depends on` line at all — no empty line, no placeholder.
   - `Parallel with #<sibling>` lines for children it can run alongside (if applicable)
   - Its `Series:` line, copied from the proposal's child block (`Series: (K/N) — previous: … | next: …`, with sibling titles; `independent` for a non-stacked child), so the whole series stays visible from any one child
   - Its own `### Size Estimate: <letter>` section — the letter, `Bound:` line and `Rationale:` from the child block's `### Size` value, written in exactly the shape a refined ticket carries (`docs/ticket-sizing.md`), so the planner inherits the child's size the same way it inherits a parent's
   - Its own `### Acceptance Criteria` section — the parent items the proposal assigned to this child, copied verbatim with their `(P<n>)` tags (its slice of the parent's partition, verified by the coverage gate above), plus the child's own criteria; omit the section only for a child the split assigned zero items

   For each child K, use the `Write` tool to create the raw title and body as plain text — `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt` (`<ticket-title> (K/N)`) and `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md` (`Related to #<original-number>\nParallel with #<sibling-number>\nSeries: (K/N) — previous: … | next: …\n\n<ticket-body>\n\n### Acceptance Criteria\n- [ ] (P<n>) <each parent item the split assigned to this child, verbatim>\n- [ ] <each of the child's own criteria>\n\n### Size Estimate: <letter>\nBound: <bound line>\nRationale: <rationale>`) — never a hand-escaped JSON literal; the title is free text and must never be interpolated directly into the command line. `<ticket-body>` here is that child's **full block** from the proposal's `### Suggested Split` — its `### Goal`, `### Decisions`, and `### Assumptions (auto-adopted)` subsections — so the created ticket is plannable without consulting the parent (AC 5); its `### Size` value is rendered as the `### Size Estimate: <letter>` section this template adds (letter, bound line and rationale split onto their own lines), and its own `### Acceptance criteria` and `### Dependencies` subsections are not repeated verbatim here, since the checklist appended by this template (sourced from the coverage-gate-verified partition — the same lines the coverage-map check read from `issue-<number>-<token>-child-K-criteria.md`), the `Parallel with #<sibling-number>` lines above, and the native blocked-by links applied after creation (below) already cover that content. **The initial template omits all `Depends on` lines; the deferred write below inserts the complete effective blocker set**, including inherited open parent blockers, after sibling numbers resolve (never blank placeholders or bracketed stand-ins). The line is inserted later, by the deferred body-file re-`Write` immediately below, right before that child's own `ensure-issue.sh ensure` call — that re-`Write` is where every blocked child's `Depends on #<blocker>` lines come from.

   Once every child's raw title/body files are written, use the `Write` tool to create a single manifest, `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-manifest.json` — a JSON array with one object per child, in creation order:
   ```
   [{"slot": "child-K-of-N", "titleFile": "${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt", "bodyFile": "${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md", "labels": ["Refined"], "milestone": null}, …]
   ```
   `labels` is the child's **seed** array only — `["Refined"]` — extended with each of `automerge:ok`, `Browser`, `ui:visual-check` the child earned at the Confirmation Gate, appended in that fixed order for each label the child earned at the gate — only the ones this child actually earned; never reorder, and never add one the gate did not compute for this child. `milestone` is always `null` in this manifest — the real inherited milestone is merged in by `ensure-issue.sh init`'s `--parent-meta` handling (see **Creation Checkpoint** above, which always passes `--parent-meta` here), not by this manifest. Call `ensure-issue.sh init` once against this manifest (per **Creation Checkpoint**) before creating any child.

   For each child K, in the same dependency order, resolve it to exactly one issue:

   **Deferred body-file write for a blocked child.** If child K has any effective blockers, re-`Write` `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md` now, immediately before the `ensure` call below, with one resolved `Depends on #<blocker>` line per member of the deduplicated union of inherited open parent blockers and blocking siblings. Parent blocker numbers come from the validated snapshot; siblings were created earlier in dependency order. Insert these lines immediately after `Related to #<original-number>` and before any `Parallel with #<sibling>` line. This includes the first child when it inherits open parent blockers. A child with no effective blockers is never rewritten. This re-`Write` is safe: `ensure-issue.sh`'s `bodyHash` is recorded at `init` but never read back — `ensure`/repair compare the live issue body against this file's current content.
   ```bash
   bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh" ensure --checkpoint .plans/.refine-<original-number>.checkpoint.json --repo <owner>/<repo> --slot child-K-of-N --title ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt --body ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md
   ```
   `ensure-issue.sh ensure` creates the child only when zero marker-bearing candidates exist for its nonce (`gh api repos/<owner>/<repo>/issues -X POST --input <payload> --jq .number`, payload composed via `jq -n --rawfile`), exactly repairs (`-X PATCH --input`) and reverifies a single drifted match against the manifest, or fails closed on ambiguity — never a duplicate create. Its exit code drives the outcome:
   - **0** — the child now exists (created, or already existed and was verified/repaired); its issue number is in the checkpoint (and in `ensure`'s own `issueNumber=<n>` stdout line). Continue to `link` below.
   - **1** — this create attempt could not be verified (a client-side timeout or a malformed response from the create). Follow the write-failure protocol: report the error, retry `ensure` once more with the exact same arguments (never a new manifest/nonce — the same checkpoint recovers the same slot via marker scan on retry), then if still failing, STOP — do not create any further children, and do not run Pass 2.
   - **10** — multiple marker matches: an unresolvable ambiguity. STOP immediately — do not retry, do not create any further children — and report the partial-state list of candidate issue numbers `ensure` printed, so a human can resolve it manually before re-running `/cenci:refine <original-number>`.
   - **12** — the candidate list itself could not be fetched, or a candidate-derived value used to decide the repair can't be read or fails validation (nothing has been written yet). Follow the write-failure protocol: report the error, retry once, then if still failing, STOP.
   - **13** — an existing issue for this slot diverged from the manifest and the repair PATCH itself failed. STOP and report the issue number `ensure` printed for manual reconciliation — do not create any further children.
   - **11** — the checkpoint is missing or corrupt, or a checkpoint field can't be read or fails validation. STOP immediately (see **Creation Checkpoint** above) — this should not happen mid-run since `init` already succeeded; do not re-run `init` against a fresh manifest, since that would mint new nonces and orphan any issue already created under the old ones. Report to the user for manual recovery.

   **Verify the title and label set persisted correctly** — `ensure` itself already re-fetched and compared these before returning 0, but re-confirm from this orchestration level too, since a JSON-escaping mistake in the manifest could still mangle a title while parsing successfully:
   ```bash
   gh issue view <child-number> --repo <owner>/<repo> --json title,labels --jq '.title, (.labels[].name)'
   ```
   If the title does not match exactly, or the label set includes a label the gate did not earn for this child or is missing one it did earn, follow the write-failure protocol: report the mismatch and STOP — do not link this child as a sub-issue, do not create any further children, and do not run Pass 2.

   **Link the child as a native sub-issue of the parent**, immediately after `ensure` succeeds for it:
   ```bash
   bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh" link --checkpoint .plans/.refine-<original-number>.checkpoint.json --repo <owner>/<repo> --slot child-K-of-N --parent <original-number>
   ```
   `ensure-issue.sh link` is idempotent — it checks the parent's existing sub-issue list first, so an already-linked child is a no-op success, never a duplicate `--parent` edit — and verifies from the parent side before returning 0. Exit **14** means the link failed (or could not be verified): follow the write-failure protocol, report the error, retry `link` once, then if still failing, STOP — do not create any further children, and do not run Pass 2. Report the partial state: which children exist (with issue numbers) and which are linked as sub-issues.

   **Mark the child's blockers**, immediately after `link` succeeds for it — follow `parent-blockers.md`'s read-before-add recovery rule, then apply one `--add-blocked-by` per missing effective blocker, using GitHub's native issue-dependency relationship (requires `gh` >= 2.94.0). Consume the same deduplicated inherited-open-plus-sibling set used for the body; every blocker already has a number:
   ```bash
   gh issue edit <child-number> --repo <owner>/<repo> --add-blocked-by <blocker-number>
   ```
   Several blockers can be applied in one call by repeating the flag, or as a comma-separated list: `--add-blocked-by 200,201`.

   The native link is authoritative for gating — the dispatch gate reads `blockedBy` directly, GitHub renders it as a real blocker in the Relationships sidebar, and it survives any later rewrite of the ticket body. It does **not** replace the child body's own `Depends on #<blocker>` lines written above: that line is a **permanent, human-visible supplement**, never a transitional shim. `mergeDependencies` (`watch/internal/dispatch/nativedeps.go`) unions the native and prose sources with native state winning on any collision, and `resolveProse` runs only for numbers not already covered natively — so a prose line duplicating an already-applied native link costs zero additional `gh` calls and zero dependency-resolution budget. Sibling children are always in the same repo, which is what native dependencies support.

   **Verify** each child's blocker set after applying it: every expected blocker from the inherited-open-plus-sibling set must be present, as required by `parent-blockers.md`. An exit-0 read with missing links is a verification failure:
   ```bash
   gh issue view <child-number> --repo <owner>/<repo> --json blockedBy
   ```
   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, then if still failing, STOP — do not create any further children, and do not run Pass 2. Report which children exist, which are linked as sub-issues, and which blocker links were applied.

   Skip this step entirely only for an empty effective blocker set (no inherited open blockers and no blocking siblings), and omit `Parallel with` lines that don't apply. Sharing a parent blocker does not impose ordering between siblings.

   #### Pass 2: Final sub-issue verification, the Coverage map, and (if ordered) an Execution Order note

   The native sub-issue list now renders the child enumeration and progress in the GitHub UI, so **do NOT append a child-ticket markdown checklist** to the parent body — there is no per-child checkbox list to write back. What the parent does gain is the `### Coverage map`: one row per numbered parent item naming the child that owns it, so a reader (and the `close` skill's parent-closure review) can see where every criterion, edge case, verification step, documentation item and boundary went.

   **(a) Final verification.** After all children are created and linked in Pass 1, re-fetch the parent's sub-issue list one last time and confirm **every** child number appears in `subIssues.nodes`:
   ```bash
   gh issue view <original-number> --repo <owner>/<repo> --json subIssues,subIssuesSummary --jq '.subIssues.nodes[].number, .subIssuesSummary'
   ```
   If any child is missing from the list, follow the write-failure protocol: report which child is not linked, retry that child's `ensure-issue.sh link --checkpoint .plans/.refine-<original-number>.checkpoint.json --repo <owner>/<repo> --slot <its-slot> --parent <original-number>` once, then verify again; if still failing, STOP and report that children #`<c1>`, #`<c2>`, … exist but child #`<cN>` is not linked as a sub-issue of parent #`<original-number>`, so the user can link it manually with `gh issue edit <cN> --parent <original-number>`.

   **(b) Post-creation coverage self-check (every split).** Re-run the coverage-map check against the children as they now exist on GitHub, not against the staged files: for each child K, fetch its live body to `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-live.md` (`gh issue view <child-number> --repo <owner>/<repo> --json body --jq .body > ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-live.md`), then run `coverage-map.sh check` with the same `--items` file as the Coverage gate and one `--child "#<child-number> (K/N)=<live file>"` per child. Exit 0 proves every parent item appears in exactly one child body **as created** and prints the map with real issue numbers in the Owner column — save that `### Coverage map` output (everything from the `### Coverage map` line on) as `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-coverage-map.md`, and record the `items=<n> owned=<n> children=<N>` counts for the Final Message. Any non-zero exit means a child was created with a body that lost an item (an escaping or truncation defect): follow the write-failure protocol — retry the fetch once, and if the check still fails, STOP and report the script's `unowned:`/`duplicate:` lines together with the partial state (which children exist, which are linked) so the user can repair the child body by hand before re-running; never close the gap by editing the parent's criteria.

   **(c) Coverage map and Execution Order.** Append the `### Coverage map` section (always, on every split) and, only when any child `Depends on` another (i.e. the children are not all independent), a concise **prose** `### Execution Order` section to the parent body — never a `- [ ]` checklist, and never a rewrite of the parent's own `### Acceptance Criteria` or `### Edge Cases`, which stay exactly as step 10 wrote them. Use the `Write` tool to create `${TMPDIR:-/tmp}/cenci/issue-<original-number>-<token>.md` with the following content (this uses `<original-number>` — parent == original — with the SAME run token from step 10, not a new one):
   ```
   <existing-body>

   ### Coverage map
   | # | Kind | Item | Owner |
   |---|---|---|---|
   | 1 | acceptance criterion | <item verbatim> | #10 (1/3) |
   | 2 | edge case | <item verbatim> | #11 (2/3) |

   ### Execution Order
   #10 first → then #11 and #12 in parallel
   ```
   The `### Coverage map` block is the verbatim output saved in (b) — never retyped. Then run:
   ```bash
   gh issue edit <original-number> --repo <owner>/<repo> --body-file ${TMPDIR:-/tmp}/cenci/issue-<original-number>-<token>.md
   ```
   **Verify** by re-fetching the parent and confirming the `### Coverage map` section (and, when written, the `### Execution Order` section) is present in the body, with one row per parent item:
   ```bash
   gh issue view <original-number> --repo <owner>/<repo> --json body --jq '.body'
   ```
   If the split has no ordering (all children independent), omit only the `### Execution Order` part — the `### Coverage map` is written regardless.

   If the parent body update or its verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that children #`<c1>`, #`<c2>`, … are linked as sub-issues of parent #`<original-number>` but the `### Coverage map` (and `### Execution Order`) did not persist, so the user can append them manually from the saved `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-coverage-map.md`.

11. **Add the "Refined" label and remove "Working":**

   **Use the effective `automerge:ok` grant for the parent computed at the `## Confirmation Gate` above (step 4) — do not recompute it here.** That computation already ANDed the refiner's `### Automation` verdict for the parent with the two skill-local safety overrides (`browserRequired` from step 8, and the **visual-check signals** match from the `frontend-classification` reference skill — the same signal set step 12 below writes) and applied the fail-closed default (an absent/empty `### Automation` entry, or any value other than exactly `grant`, is `withhold`). Steps 11-12 consume that already-computed value; they never re-derive it.

   The `automerge:ok` label was already ensured to exist before Pass 1 above.

   - If `browserRequired` is true:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --add-label "Browser" --remove-label "Working"`
   - Otherwise:
     `gh issue edit <number> --repo <owner>/<repo> --add-label "Refined" --remove-label "Working"`
   - If re-refining and `browserRequired` is false but the issue currently has the `Browser` label, also add `--remove-label "Browser"`
   - Append to whichever branch's command ran, orthogonally to the labels above: if the effective grant holds, append `--add-label "automerge:ok"`; otherwise, if the issue currently carries `automerge:ok` (re-refine withdrawing a prior grant), append `--remove-label "automerge:ok"`; otherwise append nothing.

   **Verify** by re-fetching the issue's labels and confirming the expected set — including `automerge:ok`'s presence or absence — is present/absent:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json labels --jq '.labels[].name'
   ```

   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report which labels did and didn't apply on ticket #`<number>`.

12. **Write `ui:visual-check` for the parent**: **use the parent's visual-check-signals-match result computed at the Confirmation Gate above (step 4) — evaluation already happened there; this step only performs the write.** If it matched, add the label:
   `gh issue edit <number> --repo <owner>/<repo> --add-label "ui:visual-check"`

   **Verify** by re-fetching the issue's labels and confirming `ui:visual-check` is present:
   ```bash
   gh issue view <number> --repo <owner>/<repo> --json labels --jq '.labels[].name'
   ```

   If the edit or verification fails, follow the write-failure protocol: report the error, retry once, and if still failing, STOP and report that `ui:visual-check` did not apply to ticket #`<number>`.

   This label signals to the implement skill that interactive browser verification via Playwright CLI should be used.

   **Mark this run complete.** Once step 12 has taken its action or been correctly skipped, and the write-failure protocol has not STOPped anywhere in steps 10-12, use the `Write` tool to create an empty file at `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok`. This marker is what step 13 checks before deleting anything. Confirm the write succeeded by re-reading it with `cat ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok` — if that `cat` errors, treat the marker write itself as failed (report it as such, distinct from a steps-10-12 failure) rather than letting step 13 silently read it as "absent" for an unrelated reason.

13. **Clean up this run's scoped temp files.**

   This step is reached from two distinct places: **the declined branch** (step 8 of the Confirmation Gate above, when the human chose Decline — steps 10-12 never ran at all) and **the normal post-write path** (after steps 10-12 ran, whether they all succeeded or one of them STOPped per the write-failure protocol). Handle the declined branch first, separately — it is not the same case as "marker absent" below even though both leave the `.ok` marker unwritten.

   **Declined branch.** Reached only from the gate's Decline option. The `.ok` marker is **intentionally absent** here — steps 10-12 never ran, so there is nothing to verify and no write to prove — which is different from the marker-absent case below, where steps 10-12 *did* run and one of them failed. Delete only this run's pre-confirmation scratch files (the bundle and, for a split, the parent-blocker snapshot):
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-blockers.json
   ```
   Report plainly that zero GitHub writes occurred (per the gate's step 8 — title, body, labels, assignees, milestone, and native sub-issues are state-for-state unchanged) and that re-running `/cenci:refine <number>` is how to adjust the proposal. Skip the rest of this step — do not check the `.ok` marker file, it was never expected to exist.

   For a Confirm run, check whether this run completed successfully by attempting to read the marker file:
   ```bash
   cat ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok
   ```
   If the command errors (non-zero exit, e.g. `No such file or directory`), the marker is absent. If it exits 0 (silently, since the marker is an empty file), the marker is present.

   **If the marker is present** — every write in steps 10-12 succeeded and was verified. First, clear this run's creation checkpoint (success path only — see **Creation Checkpoint** above; `clear` is idempotent, so it is harmless even when this run created no split children and no checkpoint was ever initialized):
   ```bash
   bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh" clear --checkpoint .plans/.refine-<original-number>.checkpoint.json
   ```
   Then perform `parent-blockers.md`'s scratch-file cleanup if this run split the ticket. It's safe to delete this run's temp files by explicit path (never a glob — an unmatched glob errors under some shells, and `rm -f` already ignores paths that don't exist, so listing files a given run didn't create — e.g. child files when there was no split — is harmless). Note `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-parent-meta.json` is deliberately absent from this list — `ensure-issue.sh init` already consumed and removed it itself, when it was passed:
   ```bash
   rm -f ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-bundle.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit.json ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-title.txt ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-edit-body.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-manifest.json ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-items.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-coverage-map.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-title.txt ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-body.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-criteria.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-child-K-live.md ${TMPDIR:-/tmp}/cenci/issue-<number>-<token>.ok
   ```
   Repeat the child-K paths (with the actual `K` value substituted) for each child created in Pass 1 this run.

   **If the marker is absent (and this is not the declined branch above)** — an earlier step in 10-12 did not complete successfully (the write-failure protocol already STOPped before reaching this step). Skip cleanup entirely, including the checkpoint clear above, and state explicitly to the user that cleanup was skipped for this reason: the run's `<token>`-scoped temp files are preserved for manual recovery, and the creation checkpoint (if one was initialized) is deliberately **retained** so the next `/cenci:refine <original-number>` invocation can resume from it instead of re-creating already-created issues.

### Final Message

The complete adopted proposal was already rendered once, at the `## Confirmation Gate` above, before any write — do not re-print it here. After steps 10-13 complete, present only a persistence notice plus a per-ticket verdict summary confirming that what was applied matches what was confirmed:

> Ticket #`<n>` updated. Size: `<letter>`. Labels: Refined[, Browser][, ui:visual-check][, automerge:ok]. [Created `N` child tickets: #`<c1>` (size `<letter>`, automerge: grant|withhold)[, #`<c2>` (size `<letter>`, automerge: grant|withhold)…]. Coverage map: `<items>` parent items, `<owned>` owned, `<N>` owner children — post-creation self-check passed.] [Note: cosmetic label drift was detected on the parent between the gate and the write — <what changed>; every applied label and grant/withhold decision above still matches what you confirmed, since only non-authorization-sensitive labels moved.] [Warning: ticket #`<number>`'s body was edited but the post-edit content could not be verified — please check the ticket body directly.]
>
> Every applied label and grant/withhold decision matches what you confirmed at the gate above.

Do not name or suggest the next command to run (`/cenci:implement`, etc.) here — that's covered by the **After Refinement** section below.

## After Refinement

**STOP HERE.** Your job is done. Do not:
- Enter plan mode or propose an implementation plan
- Offer to run `/cenci:implement` or start implementation
- Suggest next steps beyond what's described above

The user will explicitly invoke `/cenci:implement` when they're ready to proceed.
