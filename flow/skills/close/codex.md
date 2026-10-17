# Codex close procedure

Read `project-core`, `codex-runtime`, `shell-rules` and `subagent-safety`. The parent ticket ID
is the first argument (digits only; `#` stripped); derive and validate `owner/repo` from
`git remote get-url origin` exactly as `skills/refine/codex.md` does.

Read the parent (`gh issue view <parent> --json number,title,body,state,labels,milestone,subIssues,subIssuesSummary`).
Refuse without any write when: the parent has no sub-issues (not a split parent — close it the
ordinary way); the sub-issue graph is truncated (fail closed); or any child is not `CLOSED` —
name every open child and stop, with no override. Map every closed child to its **merged** PR
(`gh pr list --state merged --search "<child>"`, confirmed by
`gh pr view <n> --json closingIssuesReferences`); a child with no merged PR has no evidence.

Delegate the `acceptance-reviewer` role, read-only, with the parent's original acceptance
criteria, edge cases, documentation items, boundaries and `### Coverage map` numbering, its
design record (`.plans/done/<parent>-*.md` when present), and the merged child PRs — the merged
PRs' code, tests and docs are the only admissible evidence, never a child issue body or a
comment; a UI parent needs the gallery and secret-gist screenshots in en-US and it-IT per visual
item. Persist its `## Acceptance Review` table and score it with
`"${PLUGIN_ROOT}/skills/review/scripts/acceptance-verdict.sh" <table> [--ui]`. Render the
child→PR map, the table, the verdict and the planned writes, then confirm via the client's
available user-input mechanism (Confirm/Decline; Decline performs zero writes; there is no
"close anyway").

On Confirm run `"${PLUGIN_ROOT}/skills/close/scripts/close-parent.sh" --repo <owner>/<repo>
--parent <parent> --review <table>`: it re-checks every child is closed, posts the review on the
parent (banner, table verbatim, `<!-- cenci-parent-close-review -->` marker; verified by the
created comment's id), then either creates one new child under the same parent per
partial/missing item (the item verbatim as its acceptance criterion, the parent's milestone and
non-lifecycle labels, linked and verified as a native sub-issue) and exits 4 leaving the parent
open, or — with zero partial/missing items — closes the parent as completed, swaps `In Review`
for `Implemented`, verifies `CLOSED`, and exits 0. Relay any refusal or partial-state report
verbatim; never close the parent by any other means. Remove the run's temp files and stop.
