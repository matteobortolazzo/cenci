---
name: maintain
description: "Audit and repair structure, documentation, client-portability, and rule/lesson-hygiene drift through a deterministic check plus specialized agents, gated by human approval."
compatibility: Requires Claude Code AskUserQuestion, Task, and cenci project configuration.
argument-hint: [mode] [scope] [additional context]
user-invocable: true
disable-model-invocation: true
model: opus
allowed-tools: Read, Edit, Write, Grep, Glob, Task, Bash(git:*), Bash(gh:*), Bash(bash flow/skills/maintain/scripts/check.sh:*), Bash(sh flow/hooks/scripts/run-gate.sh:*), Bash(mktemp:*), Bash(mkdir:*), Bash(rm:*), Bash(cat:*), AskUserQuestion
---

> **Client dispatch**: In Codex, read `codex-runtime` and `maintain/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

## Why this skill exists

There is no unified maintenance command. Structural, documentation, client-portability, and rule
hygiene consistency each drift independently as the repo grows. This skill pairs a deterministic,
LLM-free checker (`scripts/check.sh`) with four lightweight judgment-layer agents, reports every
finding with evidence and a proposed repair, and only ever mutates the repo after explicit human
approval, in a dedicated worktree, as one reviewed PR. `rules` mode curates `## Critical Rules`
and topic-doc rule bullets — the same curation the retired garden skill used to perform, folded
into this unified flow.

## Context

Read `project-core` and resolve neutral configuration before continuing.

Read the `shell-rules`, `worktrees`, and `subagent-safety` reference skills before running any
`git`/`gh` commands, creating the Apply-phase worktree, or delegating to the analyzer agents.

**Parse `$ARGUMENTS`** into mode, scope, and additional context:
- **Mode** — the first token. `structure`, `docs`, `clients`, `rules`, or `backlog` selects that single mode.
  `all` (or an omitted/blank first token) is the default and runs the four repo-audit modes together;
  `backlog`, which mutates GitHub issues rather than repo files, is excluded from `all` and must be
  requested explicitly.
- **Scope** — the next token, if it exactly matches a project `slug` from `.cenci/config.json`'s
  `projects` array (`flow`, `watch`, `sandbox`), narrows analysis to that project. Everything else
  is optional **additional context** (focus areas or constraints).
- **Out-of-scope project no-op**: `scripts/check.sh` and every analyzer agent below are
  flow-scoped today. If `scope` resolves to `watch` or `sandbox`, treat it as an explicit
  **not yet covered** no-op for that project — report "not yet covered" and stop before Phase 2,
  rather than silently running the full flow-scoped audit against an unrelated project. This
  applies to `watch`/`sandbox` specifically; `flow` (or an omitted scope in a single-project repo)
  runs normally.

**Read-only guarantee**: Phases 1-5 (Scope, Deterministic check, Parallel audit, Report, Approval)
never write, delete, or push anything — no file mutation, no worktree, no ticket/label mutation,
no commit, no push, no pull request. Pre-approval phases must remain read-only so choosing
"report only" in Phase 5 never needs a rollback. Only Phase 6 (Apply) mutates anything, and only
after explicit approval and only inside its dedicated worktree.

## Phase 1 — Scope

Resolve `.cenci/config.json` via `project-core`. Read the root `AGENTS.md` and the applicable
project's `AGENTS.md` (`flow/AGENTS.md` when scope is `flow` or unset in a monorepo). Parse
mode/scope/additional-context per the rules above. Apply the out-of-scope-project no-op here if it
applies, and stop before Phase 2.

## Phase 2 — Deterministic check

Resolve `<repo-root>` to its absolute path and verify it names the intended checkout.
Run the repository-read-only static checker with the Bash tool's CWD set to that
verified absolute `<repo-root>` working directory:

```bash
bash flow/skills/maintain/scripts/check.sh --advisory
```

`scripts/check.sh --advisory` (see `flow/tests/maintain.test.sh`) emits its structured
JSON report on stdout and enumerates pass/warn/fail/skip results, each non-pass result
carrying a concrete fix. Parse that stdout directly; do not use `--report-file` or shell
redirection in this phase. Advisory mode explicitly skips executable/network checks so
the pre-approval read-only guarantee remains true. If it exits 2 or does not emit valid
report JSON, report the deterministic layer as incomplete and stop rather than fabricating
a result.

## Phase 3 — Parallel audit

One mode launches exactly one analyzer agent (`Task` tool); mode `all` launches all four
together, in a single message with parallel `Task` calls:

- Mode `structure` → launch only `structure-maintainer` (see `modes/structure.md`).
- Mode `docs` → launch only `docs-maintainer` (see `modes/docs.md`).
- Mode `clients` → launch only `portability-maintainer` (see `modes/clients.md`).
- Mode `rules` → launch only `rules-maintainer` (see `modes/rules.md`).
- Mode `backlog` → launch only `backlog-maintainer` (see `modes/backlog.md`); never part of `all`,
  since its apply path mutates GitHub issues rather than repo files.
- Mode `all` (default) → launch `structure-maintainer`, `docs-maintainer`,
  `portability-maintainer`, and `rules-maintainer` together.

Each analyzer only reads the repository — see `subagent-safety` before delegating — and returns
findings in the shared schema described below. No analyzer edits anything.

**Agent-failure distinction**: if a launched analyzer agent errors out, times out, or does not
return findings in the expected schema below, that is not the same thing as the agent running
clean and finding nothing — carry it into Phase 4 as an **incomplete run** for that category, not
as a silent "no findings".

## Phase 4 — Report

Categories in scope for this ticket: Structure, Documentation drift, Generated index drift, Client
mismatch, Test gap, Rule hygiene, Followup backlog.

Each finding carries: ID, category, severity, location, evidence, proposed change, repair
confidence, and required tests — the schema each analyzer agent documents in its own file.

Report any incomplete run flagged above as its own line in the summary (e.g. "Client mismatch:
incomplete — portability-maintainer errored/timed out, category not actually audited this
invocation"), separate from and never folded into that category's findings list. Phase 5's approval
options must not be read as covering, and the completion summary must not imply, "verified clean"
for a category that only produced an incomplete run — the user is choosing among what did run.

Present a chat-level summary that:
- Groups `scripts/check.sh`'s Phase 2 results by pass/warn/fail/skip and includes the checker's
  concrete fix for each non-pass result.
- Lists every agent finding grouped by category, with severity and repair confidence visible.

## Phase 5 — Approval

Ask once via `AskUserQuestion`: "Which maintenance actions should I apply?" with options scoped to
what actually ran this invocation:

- **all deterministic repairs** — apply every checker-suggested fix from Phase 2, plus every
  High/Medium-confidence finding from the agents that ran this invocation, including any Rule
  hygiene findings
- **critical+high findings** — apply only Critical/High severity findings from the agents that
  ran, including any Rule hygiene findings
- **docs+indexes only** — (offered only when `docs`-owned categories ran, i.e. `docs` or `all`
  mode) apply Documentation drift and Generated index drift findings only
- **rules only** — (offered only when `rules`-owned categories ran, i.e. `rules` or `all` mode)
  apply Rule hygiene findings only
- **let me select findings** — present the numbered finding list and let the user pick
  individually
- **report only** — apply nothing

Choosing **report only** ends the run right after the report above.

Choosing it must end after reporting: no worktree, file mutation, ticket/label mutation, commit, push, or pull request.

## Phase 6 — Apply (worktree only)

**`backlog` mode uses a different apply path.** It consolidates GitHub issues (merge duplicates, batch
small items, promote), mutating no repository files — so it creates no worktree, branch, commit, or
PR, and does not run the `scripts/check.sh`/health-gate re-verify below. For `backlog`, follow the
GitHub-issue apply path in `modes/backlog.md` instead of the worktree procedure here; the rest of
this section applies to the four repo-audit modes.

**Run token**: Generate a per-run token once, before creating the worktree, per AGENTS.md's rule
against unchecked command substitution for security-critical paths:

```bash
mktemp -u /tmp/claude/cenci-maintain-XXXXXX
```

Take the trailing `XXXXXX` portion of the printed path as `<run-token>`. Verify the command
succeeded and the token is non-empty and matches `^[A-Za-z0-9._-]+$` (rejecting `.`, `..`, or any
value containing `..`) before using it in any path. If verification fails, **stop** and report —
never fall back to an unscoped path. Carry `<run-token>` forward as literal text in every later
step; never re-derive it.

Create a dedicated worktree following the `worktrees` skill:

```bash
git -C <repo-root> worktree add .worktrees/maintain-<run-token> -b chore/maintain-<run-token> main
```

**Hard gate**: every filesystem mutation in this phase — including `Edit`, `Write`, `mkdir`, and
`rm` — MUST target an absolute path containing `/.worktrees/`. Before each mutation, verify its
resolved target satisfies that check. If any staged mutation would resolve to the main worktree,
**stop immediately** and report — do not write or delete anything, and do not rescue a stranded
edit with git commands.

Apply only the approved actions with the `Edit`/`Write` tools, touching only the locations named in
the approved findings. Every checker invocation in this phase uses the Bash tool with its
CWD set to the verified absolute `<worktree-path>` working directory. Regenerate selected
indexes (`scripts/check.sh --write` when a Generated index drift finding was approved and
`maintenance.generatedDocs` is not explicitly `false`).

**Verify the repair before shipping it**: Run the executable/default checker only now,
after approval and worktree creation, by invoking `scripts/check.sh` from that verified
absolute worktree CWD. Re-run it after any repair to confirm the repair holds, then run
flow's health gate per `docs/health-gates.md` by invoking
`sh flow/hooks/scripts/run-gate.sh flow` from the same verified absolute worktree CWD. If
either the `scripts/check.sh` re-run or
the health gate still reports fail (or warn) after the repair, **stop here** — same as the Hard
gate above — do not commit, push, or open a PR. Report the failure to the user via
`AskUserQuestion` instead: never open a PR carrying an unverified repair.

Once both re-checks are clean, review the diff, then commit and push the branch, and open the PR:

```bash
gh pr create --title "chore: maintain — <mode> <n> applied" --body-file <pr-body-path> --head chore/maintain-<run-token> --base main
```

If `gh pr create` fails because the branch already has an open PR ("a pull request for branch ...
already exists"), recover it with `gh pr view <branch> --json url -q .url` and continue — do not
treat that as an error. For any other failure, show the exact failing command and error output and
use `AskUserQuestion` to let the user resolve it before continuing (mirroring `phase-9-pr.md`'s
canonical PR-create failure contract) — never fabricate a PR number or URL.

## Completion summary

End with a chat-level summary the user can read without opening any file: the Phase 2/Phase 3
finding counts, which approval option was chosen, what was applied (or "report only — nothing
applied"), and the PR URL when Phase 6 ran.
