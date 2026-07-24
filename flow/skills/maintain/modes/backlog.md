# maintain — backlog mode

Mode `backlog` launches only the `backlog-maintainer` agent (`Task` tool) in Phase 3 — Parallel audit. No other analyzer agent runs in this mode, and `backlog` is **never** part of mode `all`: unlike the four repo-audit modes, its apply path mutates GitHub issues rather than repo files, so it must always be requested explicitly.

## Category owned

- **Followup backlog** — duplicate clusters, promotion candidates, and batchable groups among open `Followup` tickets. Sole-owned here: no other agent reports this category.

## What this mode is for

Consolidation only — merge duplicates and batch small surviving items into polish tickets so the `Followup` capture queue stays small. Read `docs/followup-triage.md` for the invariant it enforces (`Followup` = untriaged capture queue, never committed work, never release-blocking) and the promote/batch/supersede mechanics. This mode never closes a ticket for being stale; there is no expiry sweep.

## Phase 3 contribution: Inventory + Classify

This mode's contribution to the shared Parallel audit phase is `backlog-maintainer`'s Phase 1 (Inventory) and Phase 2 (Classify):

- **Inventory** — list open `Followup` tickets and the full open-issue set for cross-duplicate comparison, via read-only `gh issue list`.
- **Classify** — sort every open `Followup` into exactly one of Keep, Flag duplicate, Promote, or Batch, per `backlog-maintainer`'s evidence discipline (quoted cross-issue evidence for Flag duplicate and Batch, default Keep). There is no Close-stale action.

## Read-only

This mode's audit phase only reads the repository and GitHub (`Read`/`Grep`/`Glob` and read-only `gh issue list`/`gh issue view`) and never mutates anything — issue mutation only happens in the shared Apply phase below, after explicit approval.

## Approval

The approval options offered for this mode are the shared set defined in `SKILL.md`'s Phase 5 — Approval, scoped to the Followup backlog findings that actually ran. No new shared-menu option is added; per-finding granularity comes from the existing **let me select findings** option, since each proposed merge, promotion, and batch must be approved individually before any issue is touched.

## Phase 6 contribution — GitHub-issue apply path (no worktree, no branch, no commit, no PR)

`backlog` mode does **not** take `SKILL.md`'s worktree/commit/PR apply path — no repository files change, so there is nothing to branch, commit, or open a PR for. Apply the approved findings as GitHub-issue mutations from the main session instead, in the order below. Read `docs/skill-authoring.md` before writing any issue title or body: externally-visible free text always goes through a temp file that is read back, never interpolated inline into the command.

**Run token** (temp-file scoping only — no worktree is created): generate one with the shared

```bash
mktemp -u /tmp/claude/cenci-maintain-XXXXXX
```

and reuse its trailing token to scope this run's temp files, per AGENTS.md's rule against unchecked command substitution for security-critical paths. Verify the command succeeded and the token matches `^[A-Za-z0-9._-]+$` before using it in any path.

**Batch** (consolidate a group into one polish ticket):

1. Write the new ticket's title and body to run-token-scoped temp files. The body cites each source ticket, carries a `Supersedes #a #b #c` line, and states the combined-ticket sizing rationale (`docs/ticket-sizing.md:36-41`).
2. Create the ticket with **no** `Followup` label — a human chose to consolidate it, so it enters the backlog as a normal, unrefined ticket:

   ```bash
   gh issue create --repo <owner>/<repo> --title "$TITLE" --body-file <body-path>
   ```

3. Parse `#<new>` from the create command's output URL. If create fails, or no number can be parsed, **stop before closing any source** and never fabricate `#<new>` — report the error with the group's source numbers so nothing is lost.
4. Only after the create succeeds, close each source ticket, preserving its content in `#<new>`:

   ```bash
   gh issue close <source> --repo <owner>/<repo> --comment "Superseded by #<new> — consolidated via /cenci:maintain backlog."
   ```

   If some source closes fail after `#<new>` exists, **keep the created ticket** and report the source numbers that stayed open — they are caught on the next run; never delete or roll back `#<new>`.

**Duplicate merge** (a pure duplicate with nothing to combine), only when the user approved that specific finding:

```bash
gh issue close <dup> --repo <owner>/<repo> --comment "Duplicate of #<original>."
```

**Promote** (worth becoming real work now): remove only the `Followup` label — never auto-apply `Refined` (that is `/cenci:refine`'s job):

```bash
gh issue edit <n> --repo <owner>/<repo> --remove-label "Followup"
```

**Verify** each mutation by re-fetching the affected issue rather than by re-running `check.sh`/the health gate (no repository files changed, so neither has anything to verify):

```bash
gh issue view <n> --repo <owner>/<repo> --json state,labels
```

Confirm a superseded/duplicate source reads `state: CLOSED` and a promoted ticket no longer carries `Followup` before reporting it applied.

## Completion summary

End with a chat-level summary the user can read without opening an issue: counts by action (kept / promoted / merged / batched), the superseded-source lists per new polish ticket (`#<new>` ← `#a #b #c`), and any source that failed to close. There is **no PR URL** — this mode opens no pull request.
