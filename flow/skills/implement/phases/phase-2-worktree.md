# Phase 2: Worktree Setup

Read this file only when Phase 2 starts.

## Gate Check

Phase 2 runs when either of these holds:

- (a) the skill was **invoked** with a `.plans/<filename>.md` argument — `hasPlanFile` set to true either during mode detection itself (ticketless-mode filename, no numeric prefix) or during Pre-flight Check's Plan Verification step (ticket-mode filename, numeric prefix — mode detection only extracts the ticket ID and defers to `cenci pipeline plan-check`; see `SKILL.md`), or
- (b) the Trivial Fast Path in Phase 1 wrote a plan file this session (Trivial-Ticket Triage set `trivial = true`, and Phase 1's `## Trivial Fast Path` set `hasPlanFile = true`).

In both cases a plan file exists on disk and `hasPlanFile` is true — that is the actual invariant this gate protects, not literally "was the skill invoked with a path argument." A plan file written by Phase 1's `## New Plan` branch in the current session does NOT satisfy this gate: ordinary new-plan sessions end at Phase 1, and implementation resumes in a fresh session.

Verify:

1. Either the invocation used a `.plans/<filename>.md` path (plan file mode from mode detection), or the Trivial Fast Path ran this session.
2. The plan file exists and was read.

If either check fails, stop and tell the user to run `/cenci:implement .plans/<filename>` in a fresh session.

3. Ticket mode: record approval and enter execute. The Gate Check passing above — the human having launched this plan-file run — is what this step records as approval. Invoke `cenci pipeline plan <id> --approve` to advance to `plan_approved`, then invoke `cenci pipeline execute <id>` to advance to `executed`. Render the returned `next_actions`/`warnings`/`errors`; if either call returns non-empty `errors[]`, treat it the same as a Gate Check failure above — stop and tell the user to run `/cenci:implement .plans/<filename>` in a fresh session. `plan --approve` will self-adopt a pre-stage-tracking plan here: when the ticket's persisted pipeline stage predates or lacks `waiting_for_plan_approval` tracking (e.g. `.cenci/pipeline/` was deleted, or this plan was written before the tracking CLI existed) but the `.plans/<filename>` this session read is a valid, unambiguous match for `<id>`, the call still succeeds and lands at `plan_approved` instead of erroring. A resulting `warnings[]` entry naming the adopted plan file is informational, not a failure — do not treat it as a Gate Check failure; render it as part of the status update like any other warning. Ticketless mode: skip both calls — the pipeline commands operate on ticket IDs. The `next_actions` obtained from `execute` here are what Phases 3–5 and the Baseline Gate Check's Proceed step below render as the "what's next" status, in place of per-phase transition prose.

## Arm Goal Autopilot

The Gate Check passing means this session is committed to running phases 2–9. Arm the completion goal now, following the **Goal Autopilot (plan-file mode)** section of `SKILL.md`:

1. If the resolved config sets `cenci.goalAutopilot: false`, skip — proceed to Create Worktree.
2. Otherwise attempt to arm `/goal` directly via the `SlashCommand` tool, treating a missing tool, unknown command, or error as Goal Autopilot being unavailable. Use the plan-file-referencing condition from `SKILL.md`, substituting the actual `.plans/<filename>` for this run — that condition string already includes the 20-turn stall safety cap, so no separate arming step is needed for it here. On the unavailable outcome, print the one-line unavailable notice from `SKILL.md`, naming the cause that actually matched, and proceed without a goal.

This runs once, here — do not re-arm in later phases. Phase 9 clears the goal after the PR is created; any error gate that stops for user input clears it first (see `SKILL.md`).

## Create Worktree

Verify at least one commit exists:

```bash
git rev-parse HEAD 2>/dev/null
```

If the repository has no commits, create an initial commit:

```bash
git add -A && git commit -m "chore: initial commit" --allow-empty
```

Create the worktree:

- Ticket mode: `cenci pipeline worktree <id> --slug <description>` — creates `.worktrees/<id>-<description>` on branch `feature/<id>-<description>` (the same naming convention as before, now applied deterministically by the CLI) and records the branch and worktree path as artifacts. Render the returned `state`/`next_actions`/`warnings`/`errors`; if it returns non-empty `errors[]`, treat it as an error gate — clear the Goal Autopilot (`/goal clear` via `SlashCommand`, a no-op if none is armed) and stop, reporting the errors.
- Ticket mode, reuse trigger: when the plan file explicitly names an existing worktree/branch to reuse (rather than calling for a fresh `.worktrees/<id>-<description>`), use `cenci pipeline worktree <id> --attach <path>` instead of `--slug` — it validates `<path>` against `git worktree list --porcelain`, records the branch/worktree path as artifacts exactly like `--slug` does, and creates nothing. Render the returned `state`/`next_actions`/`warnings`/`errors` the same way; a non-empty `errors[]` is the same error gate as the `--slug` path above — clear the Goal Autopilot and stop, reporting the errors.
- Ticketless mode: `git -C <repo-root> worktree add .worktrees/<auto-slug> -b feature/<auto-slug> main` — unchanged; the pipeline CLI operates on ticket IDs, and ticketless mode has none.

All subsequent phases run inside the worktree. Use absolute paths rooted at `<worktree-path>` when delegating file edits.

## Baseline Gate Check

Before handing off to Phase 3, confirm the worktree's own baseline is green — a repository that's already broken shouldn't have new work piled on top of it.

### 1. Applicability

Run this check only when the resolved config defines a top-level `gateCommand` (single-repo) or at least one `projects[]` entry (monorepo). If neither is present, skip this section silently and proceed to Phase 3 — there is nothing to gate on.

### 2. Resolve targets

- **Single-repo**: one target, invoked with no slug.
- **Monorepo**: map the plan's affected components to the set of matching `projects[].slug` values:
  - Primary signal: the plan file's `## Project Context` section — for each project whose `AGENTS.md` was bundled there, read its leading `# Project: <name>` header and match `<name>` against `projects[].name` to get that entry's `slug`.
  - Fallback: the `### Technical Notes` "Affected services"/"Affected components" lines inside `## Ticket Details`, matched against each `projects[]` entry's `slug`, `name`, or `path`.
  - Deduplicate the resolved slugs. If nothing resolves (no match found either way, or only some affected components matched), this is a no-op for the unmatched portion, not an error — proceed to Phase 3 unchanged. This mapping is inherently fuzzy (free text matched against config), so when it under-matches, print a one-line notice such as "Baseline gate: could not map this ticket's affected components to a configured project — skipping the baseline check" so silent under-coverage stays visible in the chat-level summary rather than going unnoticed.

### 3. Invoke

For each resolved target, run it as its own single Bash call — one subshell that both `cd`s to the absolute worktree path and runs the script, never relying on an inherited `cd` from a prior call:

```bash
( cd "<abs-worktree-path>" && sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/run-gate.sh" "<slug>" )
```

Omit the trailing `"<slug>"` argument entirely for the single-repo case; pass the resolved, quoted `projects[].slug` value for each monorepo target.

### 4. Interpret

Parse stdout for the `GATE_STATUS=` line:

- `GATE_STATUS=green` or `GATE_STATUS=unset` → this target passes.
- `GATE_STATUS=red` → this target fails as **"gate failed"** — capture the project slug (or "top-level" for single-repo) and the command's output.
- Non-zero exit with **no** `GATE_STATUS=` line at all → this target fails as **"gate could not run"** — capture the project slug and stderr/output. This is a script error (missing `jq`, malformed config, missing project directory, no-match/ambiguous slug), distinct from a red gate, and must be reported with visibly different wording than the "gate failed" case above.

### 5. Proceed

If every invoked target passes (green or unset), continue to Phase 3 — render the `execute` stage's `next_actions` obtained at the Gate Check above (see `## Gate Check`, step 3) as the status update; no other observable change to the pipeline.

### 6. Stop on failure

If any target fails (either "gate failed" or "gate could not run"):

1. Run `/goal clear` via the `SlashCommand` tool first — a no-op if nothing is armed (see `SKILL.md`'s Goal Autopilot "Clearing" subsection).
2. Hard-stop and report which project's gate failed (or could not run), using the distinct wording from step 4, plus its captured output. Do not call `AskUserQuestion` — there is no choice to offer here, only a report.
3. State explicitly that the worktree and branch are left in place, and that the user should fix the baseline and re-run `/cenci:implement .plans/<filename>` to retry.
