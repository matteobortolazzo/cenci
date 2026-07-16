---
name: garden
description: "Curate AGENTS.md critical rules and topic docs through a reviewed PR."
compatibility: Requires Claude Code AskUserQuestion and cenci project configuration.
argument-hint: [project-name] [additional context]
user-invocable: true
disable-model-invocation: true
model: opus
allowed-tools: Read, Edit, Write, Grep, Glob, Bash(git:*), Bash(gh:*), Bash(cat:*), Bash(mkdir:*), Bash(mktemp:*), Bash(rm:*), AskUserQuestion
---

> **Client dispatch**: In Codex, read `codex-runtime` and `garden/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

## Why this skill exists

The lessons-collector only ever **appends**: every incident adds a rule to a
`## Critical Rules` section or a `docs/<topic>.md` file, and nothing ever removes
one. CLAUDE.md is loaded into every session, so each stale, duplicated, or
superseded rule is a permanent context tax that dilutes the rules that still
matter. This skill is the decay half of the lesson lifecycle: it audits the
accumulated rules, proposes a curation plan, and — after human approval — applies
it as a normal reviewed PR. It never silently deletes knowledge.

## Context

Read `project-core` and resolve neutral configuration before continuing.

Use the config returned by `project-core`. Note `isMonorepo` and `projects`; curate shared
critical rules in AGENTS.md.

**Parse `$ARGUMENTS`:**
- If the first token exactly matches a project `name` from the config's `projects` array, treat it as a **project filter**: garden only that project's rule sources. Everything after it is optional user context.
- Otherwise all of `$ARGUMENTS` is optional **user context** (focus areas, e.g. "only the testing rules", or constraints, e.g. "don't touch archived topics").

**Shell rules**: Read the `shell-rules` skill before running any `git` or `gh` commands. Read the `worktrees` skill before creating the worktree in the Apply phase.

**Run token**: Generate a per-run token once, before any temp file or worktree is created:

```bash
mktemp -u /tmp/claude/cenci-garden-XXXXXX
```

Take the trailing `XXXXXX` portion of the printed path as `<run-token>`. Verify the command succeeded and the token is non-empty and matches `^[A-Za-z0-9._-]+$` (rejecting `.`, `..`, or any value containing `..`) before using it. If verification fails, **stop** and report — never fall back to an unscoped path. Carry `<run-token>` forward as literal text in every later step; never re-derive it.

## Phase 1 — Inventory

Enumerate every rule source in scope (honoring the project filter when set):

1. **CLAUDE.md Critical Rules** — the file at `claudeMdLocation`, plus each project's own `CLAUDE.md` when `isMonorepo` is true. Collect the bullets under `## Critical Rules`.
2. **Topic docs** — rule bullets in `docs/*.md` at the repo root and in each in-scope project's `docs/` directory. Only bullets that state rules or conventions are in scope; narrative documentation (setup guides, architecture prose) is not.
3. **Legacy lessons files** — `.claude/rules/lessons-learned.md` and `.claude/rules/lessons-learned-<slug>.md` if present. These are read-only fallbacks that new tooling no longer writes to; they are candidates for migration (Phase 2).

Do NOT treat other `.claude/rules/` files as in scope — that directory is reserved for files explicitly `@`-imported by CLAUDE.md.

Report a short inventory to the user before auditing: each source file and its rule-bullet count.

## Phase 2 — Audit

Classify every in-scope rule into exactly one action. **Default is Keep**: when in doubt, keep — a stale-looking rule is cheaper than a repeated incident.

| Action | Meaning | Evidence required |
|---|---|---|
| **Keep** | Still accurate, load-bearing, and concise | None |
| **Tighten** | Keep the rule, rewrite the text: incident narrative → one actionable imperative sentence. Preserve meaning, file paths, and issue refs like `(#357)` | The rewrite must not drop any constraint the original stated |
| **Merge** | Two or more bullets cover the same failure mode (possibly across files) → one combined rule in the most specific home | Quote each bullet being merged |
| **Relocate** | The rule is real but not a project-wide invariant → move it from always-loaded `## Critical Rules` to the matching on-demand `docs/<topic>.md` | Name the target topic file; create it per the lessons-collector template only when 2+ relocated rules share the topic |
| **Demote** | An automated check now enforces the rule (regression test, lint rule, CI gate, runtime guard) → replace the prose rule with a one-line pointer to the check in the relevant `docs/<topic>.md`, and remove it from Critical Rules | `Grep` this run for the enforcing test/check and quote its path. If the audit cannot find the check, the rule stays **Keep** |
| **Archive** | The rule references code, flags, files, or workflows that no longer exist, or a later rule supersedes it → remove it | `Grep` this run showing the referenced symbol/path is gone (or quote the superseding rule) |

**Evidence discipline**: Demote and Archive require fresh `Grep`/`Read` evidence gathered during this run — never from memory of the codebase. Record the evidence (file path or quoted rule) next to each proposal; it goes into the PR body.

**Legacy migration**: For each legacy `lessons-learned*.md` file found in Phase 1, audit its entries with the same table. Propose moving the surviving entries into their proper homes (`docs/<topic>.md` or Critical Rules) and deleting the legacy file in the same PR, so the fallback path can finally retire.

## Phase 3 — Proposal gate

Present the full plan as a markdown table: source file, rule (first ~15 words), proposed action, evidence. Group rows by action. If **every** rule is Keep, report "Nothing to garden — all N rules are current" and **stop** (no worktree, no PR).

Then ask with `AskUserQuestion` (multiSelect): "Which curation actions should I apply?" with one option per non-empty group:

- **Tighten & Merge** — wording cleanups and duplicate consolidation
- **Relocate & Demote** — move rules out of always-loaded context
- **Archive** — remove rules whose subject no longer exists
- **Legacy migration** — move surviving legacy entries home and delete the legacy file

Apply only the selected groups; everything else stays untouched. If the user selects nothing (or answers via Other with an abort), report "No changes applied" and **stop** without creating a worktree. If the user's Other-text asks for item-level control, re-present the affected group as one or more follow-up `AskUserQuestion` calls before applying.

## Phase 4 — Apply (worktree only)

Create a dedicated worktree following the `worktrees` skill:

```bash
git -C <repo-root> worktree add .worktrees/garden-<run-token> -b chore/garden-lessons-<run-token> main
```

**Hard gate**: every filesystem mutation in this phase — including `Edit`, `Write`, `mkdir`, and `rm` — MUST target an absolute path containing `/.worktrees/`. Before each mutation, verify its resolved target satisfies that check. If any staged mutation would resolve to the main worktree, **stop immediately** and report — do not write or delete anything, and do not rescue a stranded edit with git commands. In particular, delete legacy lessons files only with `rm <absolute-worktree-path>` after this check; never pass `rm` a relative path.

Apply the approved actions with the `Edit` tool:

- Touch only the rule bullets named in the plan; never reflow or reformat surrounding content.
- Merged and relocated rules land in their new home in the same edit batch that removes them from the old one — a rule must never be absent from both files at any commit.
- Demoted rules leave a pointer in the topic doc, e.g. `- Codified: <one-line rule> — enforced by <test path>`.

## Phase 5 — Commit, push, PR

Commit in the worktree with message `chore: garden lessons — <n> tightened, <n> merged, <n> relocated, <n> demoted, <n> archived` (omit zero counts).

Push with `git push -u origin chore/garden-lessons-<run-token>`. If the push fails, show the exact error and ask with `AskUserQuestion` ("Retry" / "Abort — leave worktree for manual push") before proceeding; never continue past an unpushed branch.

Write the PR body with the `Write` tool to `/tmp/claude/cenci-garden-<run-token>-pr-body.md` (create `/tmp/claude` first if needed), then verify it is non-empty with `cat` before use. The body lists every applied change grouped by action, each with its evidence line, plus a "Not touched" note for groups the user declined. Create the PR:

```bash
gh pr create --title "chore: garden lessons" --body-file /tmp/claude/cenci-garden-<run-token>-pr-body.md --head chore/garden-lessons-<run-token> --base main
```

If `gh pr create` fails with "a pull request for branch ... already exists", recover the URL with `gh pr view <branch> --json url -q .url` and continue. For any other failure, show the exact failing command and error output and ask with `AskUserQuestion` ("Created manually, continue" / "Abort") — never fabricate a PR number or URL. After the PR exists, remove the temp body file.

## Completion summary

End with a chat-level summary the user can read without opening any file: counts per applied action, the PR URL, which groups were skipped, and any Demote/Archive candidates that stayed Keep for lack of evidence (so the user can codify them deliberately later).

## When to run

- A `## Critical Rules` section exceeds ~10 bullets, or a `docs/<topic>.md` exceeds ~25 rule bullets (the lessons-collector suggests this in its summary when a session pushes a file past these marks).
- A regression test or lint rule was added that codifies an existing prose rule.
- Quarterly, as routine hygiene — an all-Keep audit is a successful, cheap run.
