# Codex maintain procedure

Read `project-core`, `codex-runtime`, `shell-rules`, `subagent-safety`, and `worktrees`.
The planning invocation begins in `/plan`; Phases 1–5 are repository-read-only and make
no file, worktree, checkpoint, ticket, label, commit, push, or pull-request mutation.
When the user approves an applying choice, return the approved finding IDs and repairs
as the apply plan and instruct a second normal-mode invocation that passes that plan
explicitly. Keep authenticated mutations and every user question in the root agent.

## Phase 1 — Scope

Parse the first argument as the mode when it is `structure`, `docs`, `clients`, `rules`,
`backlog`, or `all`; blank defaults to `all`. Mode `backlog` mutates GitHub issues rather
than repo files, so it is excluded from `all` and must be requested explicitly. Parse the
next token as scope only when it exactly matches a configured project slug. The remaining
text is additional context.

Read the root and applicable project `AGENTS.md` files. If scope is `watch` or `sandbox`, report `not yet covered` and stop before Phase 2. Scope `flow`, or omitted scope in the configured repository, continues.

## Phase 2 — Deterministic check

Resolve `<repo-root>` to an absolute path and verify it is the intended checkout. Run `bash flow/skills/maintain/scripts/check.sh --advisory` with the shell tool working directory set to the verified absolute `<repo-root>`. Parse the JSON from stdout; do not use `--report-file` or redirect it into the repository. Advisory mode is the only checker mode allowed before approval because it runs repository-read-only static checks and explicitly skips executable and network checks.

If the checker exits 2, emits malformed JSON, or otherwise cannot provide a structured
summary, report the deterministic layer as incomplete and stop before worker delegation;
never invent a clean result.

## Phase 3 — Worker audit

Delegate only read-only analysis, including `## Critical Rules` / topic-doc rule curation,
using generated Codex agent adapters when available or built-in workers with the same
bounded prompts from `flow/agents/*-maintainer.md`:

- Mode `structure` launches only `structure-maintainer`.
- Mode `docs` launches only `docs-maintainer`.
- Mode `clients` launches only `portability-maintainer`.
- Mode `rules` launches only `rules-maintainer`.
- Mode `backlog` launches only `backlog-maintainer`; it is never part of `all`, since its
  apply path mutates GitHub issues rather than repo files.
- Mode `all` launches all four workers in parallel.

Give every worker the resolved mode, scope, additional context, applicable mode file,
the deterministic JSON, and the exact eight-field finding schema documented by its
maintainer prompt. Workers may read and search only; they never edit files, mutate
GitHub, create worktrees, or ask the user questions.

Treat an errored, timed-out, or malformed worker result as an incomplete run for that
worker's categories. Do not translate it into “no findings,” retry it as a different
category, or imply that category was verified clean.

## Phase 4 — Report

Present the deterministic results grouped by pass, warn, fail, and skip, including each
non-pass result's concrete fix. Then list worker findings grouped by Structure,
Documentation drift, Generated index drift, Client mismatch, Test gap, Rule hygiene, and
Followup backlog, with severity and repair confidence visible.

List every incomplete worker category separately with its reason. Completion counts and
approval scope include only checks and workers that actually completed.

## Phase 5 — Approval

Ask exactly once with Codex's available user-input mechanism which actions to apply.
Offer only choices supported by the mode and findings that actually ran:

- **all deterministic repairs** — every deterministic repair plus High/Medium-confidence
  worker findings.
- **critical+high findings** — only Critical/High worker findings.
- **docs+indexes only** — only when mode `docs` or `all` ran.
- **rules only** — only when mode `rules` or `all` ran.
- **let me select findings** — present numbered finding IDs for explicit selection.
- **report only** — apply nothing.

Report only is terminal: do not create a worktree, mutate files or GitHub, commit, push, or open a PR. For an applying choice, emit an explicit apply plan containing the mode,
scope, completed/incomplete categories, selected finding IDs, and exact proposed repairs.
The normal-mode invocation must receive this approved plan explicitly; it must not expand
the selection or silently substitute a new audit.

## Phase 6 — Apply

Mode `backlog` uses a different apply path: it consolidates GitHub issues (merge duplicates,
batch small items, promote) and mutates no repository files, so it creates no worktree,
branch, commit, or PR, and skips the `check.sh`/health-gate re-verify — follow the
GitHub-issue apply path in `modes/backlog.md`. Its title-carrying polish-ticket create uses
`gh api ... --input` with a payload `jq`-composed from file-tool-authored raw title/body
inputs, never an inline `--title` and never a hand-escaped JSON literal. The rest of this
section applies to the four repo-audit modes.

Verify the normal-mode invocation includes the approved apply plan. Persist a maintain
checkpoint, then generate and validate one run token following the shared worktree
procedure; reject an empty, dot-only, traversal-containing, or non-portable token. Create
the dedicated worktree from the verified repository root and record its resolved absolute
path as `<worktree-path>`. Arm the native goal only after both the approved plan and
worktree are established.

Create the dedicated worktree before any repository mutation. Every edit and generated
file must resolve beneath the absolute `<worktree-path>` and contain a `/.worktrees/`
segment. Apply only the selected repairs. Run `check.sh --write` only when a selected
Generated index drift repair requires it and `maintenance.generatedDocs` is not explicitly
`false`.

Every checker invocation in this phase uses the shell tool working directory set to the verified absolute `<worktree-path>`. Run the executable/default checker only after approval:
`bash flow/skills/maintain/scripts/check.sh`. Re-run it after any approved `--write` or
manual repair, and require a structured summary with zero warn/fail results. Then run
Flow's configured health gate from the same absolute worktree according to
`docs/health-gates.md`.

If the checker crashes, any selected finding remains, the checker reports warn/fail, or
the health gate is not green, checkpoint the run, clear the goal, retain the worktree and
branch, and stop without committing, pushing, or opening a PR. Report the distinct failure
and its captured output.

After both verification layers are clean, review the diff, commit, push the branch, and
open one PR containing only the approved maintenance repairs. Recover an already-open PR
for the branch by reading its real URL; never fabricate one. Clear the goal only after the
PR exists. Finish with deterministic and worker finding counts, incomplete categories,
the approval choice, applied finding IDs, verification results, and the PR URL.
