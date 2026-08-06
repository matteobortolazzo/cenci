# Comment attribution — banners and `<!-- cenci-<kind> -->` markers

`gh` posts every flow-initiated comment under the human operator's own GitHub
identity, so without extra framing a cenci-authored comment (an escalation
question, a plan copy, a review report) is indistinguishable from something
the human typed themselves. `watch`'s Go helpers already solve this for their
own comments (`watch/internal/dispatch/reconcile.go`'s `cenciMarkerPrefix` and
the visible attribution line each posted message opens with); this doc
extends the same convention to every comment `flow`'s skills post, and is the
registry a future call site must update in the same PR (#951).

**The banner is human-facing attribution only, never a trust signal.**
Because `gh` posts under the operator's own identity and the banner literal
is public (it is committed, byte-exact, in this repo), any GitHub user can
post a comment carrying a byte-identical banner. No consumer today treats the
banner itself as proof of cenci authorship, and none ever should: the only
machine-trustworthy signals are the `<!-- cenci-<kind> -->` marker — read
*after* `stripBlockquoteLines` strips every blockquoted line, so a forged
banner-only comment with no real marker never passes — and, for the one
nonce-bearing kind, the posting comment's own immutable numeric ID.

## The convention

Every flow-posted comment body opens with a **blockquoted** cenci attribution
banner naming the skill/phase that produced it:

```markdown
> 🤖 **cenci** — <what> posted by `/cenci:<skill>` (<phase>).
```

Every flow-posted **issue** comment additionally embeds a hidden
`<!-- cenci-<kind> -->` marker, with `<kind>` distinct per call site, on its
own **non-blockquoted** line elsewhere in the body (never inside the
blockquote).

### Why the marker must never be blockquoted

`stripBlockquoteLines` (`watch/internal/dispatch/resume.go`) strips every
`>`-prefixed line out of a comment body *before* `isCenciAuthored` scans for
the `<!-- cenci-` prefix, and before the escalation anchor's own nonce check
runs. A blockquoted banner is safe — it is never inspected for markers or
nonces, so its exact wording is free to read naturally as prose. A
blockquoted marker would be invisible to every one of those consumers: it
would never be detected as cenci-authored, and an anchor placed inside a
blockquote would never be found by the nonce scan either. This is why the
banner and the marker/anchor are always split across separate lines with
different quoting — the banner blockquoted, the marker/anchor bare.

### `<kind>` registry

Every marker-bearing flow comment uses one of the `<kind>` values below;
`<kind>` is unique per call site so a consumer can tell which flow code path
produced a given comment. flow's five kinds:

| Kind | File / call site | Marker |
|---|---|---|
| `plan-comment` | `skills/implement/phases/phase-1-plan.md` — `## Persist the Plan`'s `planComment` step | `<!-- cenci-plan-comment -->` |
| `design-summary` | `skills/design/SKILL.md` — Step 7B (Step 7C reuses the same banner-carrying file for each dependent post) | `<!-- cenci-design-summary -->` |
| `parent-gap-report` | `skills/implement/phases/phase-9-pr.md` — the parent acceptance-criteria gap report | `<!-- cenci-parent-gap-report -->` |
| `followup-tracked` | `skills/implement/phases/phase-9-pr.md` — the followups-tracked comment | `<!-- cenci-followup-tracked -->` |
| `planner-escalation` | `skills/implement/phases/phase-1-plan.md` — `## Escalation Anchor` and its four call sites | `<!-- cenci-planner-escalation:<nonce> -->` |

`watch`'s four pre-existing kinds, recorded here for reference only — they
are `watch`'s own convention (`cenciMarkerPrefix`,
`watch/internal/dispatch/reconcile.go`), unaffected by this ticket, and never
appear under `flow/skills/`:

| Kind | Marker |
|---|---|
| `dispatch-attempt` | `<!-- cenci-dispatch-attempt -->` |
| `dispatch-failed` | `<!-- cenci-dispatch-failed -->` |
| `plan-invalid` | `<!-- cenci-plan-invalid -->` |
| `reconcile-stuck` | `<!-- cenci-reconcile-stuck -->` |

### `planner-escalation` is the one nonce-bearing kind

Every other kind's marker is a fixed literal. `planner-escalation` is the
sole exception: its marker carries a per-escalation nonce
(`<!-- cenci-planner-escalation:<nonce> -->`, see
`skills/implement/phases/phase-1-plan.md`'s `## Escalation Anchor`) because
that comment doubles as the durable anchor a resumed session or `cenci
dispatch` must locate by exact identity, not merely recognize as
cenci-authored. It needs no *second*, plain `<!-- cenci-planner-escalation -->`
marker alongside the nonce-bearing one — the nonce-bearing marker already
carries the `<!-- cenci-` prefix `isCenciAuthored` scans for, so it already
satisfies the marker invariant on its own.

## Why every issue comment gets a marker (defense in depth)

This is defense in depth, not a fix for a reproducible bug today.
Answer-detection treats *"positioned after the anchor + no `<!-- cenci-`
marker + non-bot author + write permission"* as the human's answer
(`skills/implement/phases/phase-1-plan.md`'s `## Resume From Draft` step 2).
flow's marker-less comments did not previously collide with this rule only
by ordering happening to save them — design comments precede implement, and
while a ticket sits `awaiting-input` the flow session has stopped, so the
only comments still arriving are `watch`'s, which are all marked already.
That safety was incidental, not structural: any future flow comment posted
on a ticket after its anchor, under the operator's own account, would
satisfy every clause of the human-answer test. Marking every flow-posted
issue comment makes the invariant enforced rather than emergent, and the
marker doubles as the machine-readable half of the attribution banner.

## Other `<!-- cenci-*` markers under `flow/skills/`

Two other hidden-HTML-comment markers already exist under `flow/skills/`,
predating this ticket and unrelated to the comment-attribution banner
convention above — recorded here only so this doc stays the complete,
sync-checked enumeration of every `<!-- cenci-<kind> -->`-shaped marker in
the tree, not because either one carries an attribution banner:

| Kind | File / call site | Marker | What it actually is |
|---|---|---|---|
| `maintain` | `skills/maintain/scripts/check.sh` | `<!-- cenci-maintain:<id>:start -->` / `<!-- cenci-maintain:<id>:end -->` | A paired start/end marker delimiting an auto-generated section of `flow/README.md` (e.g. the docs-nav block) — not a GitHub comment at all. |
| `refine-create` | `skills/refine/scripts/ensure-issue.sh` | `<!-- cenci-refine-create:<nonce> -->` | The pre-existing issue-creation dedup marker `/cenci:refine` embeds in a newly created child/companion issue's **body** (never a comment), so a resumed refine session can recover the same issue by nonce instead of re-POSTing. |

## PR comments: banner only, no marker

Two call sites post to a **PR** thread rather than the ticket's issue
thread — `skills/review/SKILL.md`'s Phase 4 report and
`skills/address-review/SKILL.md`'s general PR comment. Both carry the
attribution banner but no `<!-- cenci-<kind> -->` marker. The marker
invariant this doc documents is issue-thread-scoped: `classifyComments`
(`watch/internal/dispatch/resume.go`) only ever scans
`repos/<owner>/<repo>/issues/<number>/comments` on the ticket itself, never a
PR's own comment thread, so a marker on a PR comment would never be read by
any consumer — it would be inert weight with no compensating benefit.
