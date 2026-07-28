# Codex refine procedure

Read `project-core` and `codex-runtime`. Require `/plan`. Gather ticket context, classify
frontend/design scope, ask material scope and acceptance questions, and produce the refined
ticket proposal. Do not edit GitHub in Plan mode. Hand off `$cenci:refine apply <ticket>
<approved-plan>` to normal mode, then update the ticket and labels and clear the checkpoint.
When a split is applied, link each child of the split to the parent as a native GitHub sub-issue
(`gh issue edit <child> --parent <parent>`) — do not append a child-ticket markdown checklist; the
native sub-issue list carries the enumeration.

Divergence: the refiner agent split is Claude-only — Codex has no subagent model tiering, so
this native procedure performs the refinement analysis inline as described above.

**Command surface (least privilege)**: this workflow's own procedure performs no remote
fetches of its own — neither a `curl` grant nor a web-fetch capability, and the procedure
invokes neither. (Attachment downloads via the `attachments` reference skill may still use
`curl` when the user selects an attachment; that call falls outside this narrowed grant and
will prompt for approval — an accepted tradeoff, not a regression.) Its `gh` surface is
limited to exactly two `gh issue` verbs — `view` and `edit` — and no other verb (refine posts
no comments and never lists/closes an issue), plus `gh label create …`, `gh api user --jq …`
(via `ticket-ownership`), and `gh api repos/…`; its `git` surface is limited to `git remote
get-url`; and its payload-composition surface is a standalone `jq -n --rawfile …` call, per
the `shell-rules` skill's canonical snippet. The only temp-name primitive is a standalone
`mktemp -u /tmp/claude/…` call — a dry-run name generator, never `mktemp -d`; the file tool
creates the actual file, and the printed token is carried forward as literal text, never
shell state. Every title-carrying issue write (the retitle edit, each child-ticket create,
and the companion design-ticket create) goes through `gh api repos/<owner>/<repo>/… -X
PATCH|POST --input <json-file>` with a payload `jq`-composed from file-tool-authored raw
title/body inputs — never an inline `--title` and never a hand-escaped JSON literal.
