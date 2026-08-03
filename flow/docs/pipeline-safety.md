# Pipeline safety — restart and risk-profile rules

Conventions for `/cenci:implement` and other multi-phase pipelines where a step can
fail mid-run or get reused in a new context.

## Rules

- A mandatory-restart rule (e.g., "any re-entry must restart at Rebase") only prevents
  one failure mode. Each downstream step that could itself fail needs its own documented
  recovery or idempotency handling — don't assume the first step's handling covers later
  ones. A rebase rewrites commit SHAs, so a resumed push needs `--force-with-lease` retry
  handling; re-creating an already-existing PR needs `gh pr view` recovery. Missing
  recovery at downstream steps causes operational failures on autopilot resume.

- When a new skill section or automated path reuses an existing error-handling or safety
  rule by reference but changes what happens after that step (e.g., removes a human
  checkpoint, continues into further autonomous phases, or arms an unattended completion
  loop), re-evaluate and explicitly restate the error-handling for the new risk profile.
  The original rule may have worked at its original stopping point but be under-specified
  for the new path. Three named instances of this rule in `/cenci:implement`: the Trivial Fast
  Path (`skills/implement/phases/phase-1-plan.md`'s `## Trivial Fast Path`), lean
  planning (`planning.autonomy: "lean"`, `skills/implement/phases/phase-1-plan.md`'s
  `## Lean Approval Path`) — both reuse `## Persist the Plan`'s write/comment/label/artifact
  machinery but restate its verification and error-surfacing rules for a checkpoint-free,
  same-session continuation into Phase 2 — and the unattended planner escalation path
  (`skills/implement/phases/phase-1-plan.md`'s `## Unattended Escalation Path`), which reuses
  the same `## Persist the Plan` assemble-don't-re-emit machinery but restates its
  verification/error-handling for a *different* new risk profile: not a checkpoint-free
  continuation into further phases, but a path that posts a ticket comment and swaps board
  labels — each with its own ordering constraint and per-step recovery/idempotency
  documented — before stopping cleanly at Phase 1, never reaching Phase 2 at all.

- When a restatement of a multi-step procedure explicitly claims to mirror or model an
  existing case or section (e.g. "exactly as case (iii) does" or "mint a fresh nonce, then
  fall through to the write-questions-file/post sub-steps below"), verify the restatement
  includes every intermediate state-persistence step and validation action, not just the
  terminal action or outcome. A restatement focused on matching the final behavior can
  silently drop a load-bearing intermediate step (e.g. "persist the nonce before posting")
  that is essential to recovery on retry; this creates an orphaned or incorrect anchor state.
  Concrete instance: `/cenci:implement`'s `## Resume From Draft` step 3 author-mismatch
  branch (#880), which claimed to model `## Repair Escalation Anchor` case (iii)'s
  nonce-posting sequence but skipped the intermediate persist-nonce-to-front-matter step,
  leaving an unverified fresh nonce published while the draft retained a stale persisted
  nonce — an unmappable state that enabled further validation bypasses on retry.

- All shared temp files written by phases or agents (e.g.
  `/tmp/claude/cenci-<ticket-id-or-slug>-diff.patch`) must be uniquely scoped by worktree
  path, run ID, or session UUID. Fixed paths without scoping let multiple concurrent
  `/cenci:implement` jobs in the same monorepo silently overwrite each other's state, so a
  reviewer can end up analyzing the wrong diff or broken context.

- When a new checkpoint-free path cites an existing path as its structural model, explicitly
  verify that deterministic safety backstops (checks that can only disqualify the path, never
  promote it) present in the model are also reused in the new path — e.g., the sensitive-path
  pattern-match backstop in the Trivial Fast Path. The model's sub-patterns may not be
  mandatory in all paths but become essential when the new path removes a human checkpoint or
  arms autonomous continuation. Such backstops gate safety-critical file changes and detect
  mismatches that planner judgment alone cannot catch.

- Session-scoped safety flags that guard autonomous continuation (e.g., a sticky "escalated"
  flag that blocks checkpoint-free approval once any escalation question fires) must be backed
  by durable, independently-verifiable state on disk, checked with deterministic logic, not
  solely by in-context model recall. Context compaction or subagent re-invocation between turns
  can silently lose in-memory state; durable on-disk state survives the gap and enables
  fail-closed behavior (treat inconclusive checks as "gate active" rather than "gate absent").
  The durable state need not be a dedicated marker file: `/cenci:implement`'s Lean Approval
  Path (`skills/implement/phases/phase-1-plan.md`) uses the persisted `status: awaiting-input`
  draft itself — plus its `escalationNonce`/`escalationCommentId` anchor fields — as this
  durable state (#849), checked with a deterministic on-disk backstop over `.plans/<id>-*.md`
  rather than a second, separately-maintained bookkeeping file that could drift out of sync
  with the draft it was meant to describe.

- When delegating work to a subagent that must edit repo-root config files (e.g.
  `.mcp.json`, `.claude/settings.json`) as part of a feature-worktree PR, delegate targeting
  the worktree's own root paths (at identical relative location), never the literal
  main-checkout absolute paths. The feature worktree mirrors the repo structure; delegating
  to `/workspace/...` (main checkout) will violate `guard-main-worktree.sh` safety blocks by
  design. Instead, delegate to `/workspace/.worktrees/<id>-<desc>/.mcp.json` etc. These
  changes will commit and ship with the PR branch exactly as intended.

- Durable recovery state — state a *resumed* run must read back to avoid repeating a
  destructive or non-idempotent action (e.g., a create) — is keyed by the resource it
  recovers, not by the run that wrote it, and lives in `.plans/`, never a run-scoped or
  `/tmp` bookkeeping file. This is the mirror image of the run-scoping rule above (all
  *transient* shared temp files must be uniquely scoped by worktree path, run ID, or session
  UUID): a transient temp file's whole purpose is to never collide with, or be mistaken for,
  another run's — but recovery state has the opposite requirement. It must be *found* by a
  second, independent invocation (a retry after a crash, a resumed session under a fresh
  temp-file token, or a different identity resuming a stalled run) that has no memory of the
  first run's ID or token, so it can only be keyed by the stable identity of the thing it is
  protecting — here, "this repo's issue #`<parent>`" — never by a per-attempt token that a
  second invocation has no way to reconstruct. Concrete instance: `/cenci:refine`'s creation
  checkpoint (`skills/refine/scripts/ensure-issue.sh`, #876, 2/12 of #661), which makes split-
  child and companion-design-ticket creation recoverably idempotent across timeouts, retries,
  crashes, and resumed refinement sessions. It lives at
  `.plans/.refine-<parent>.checkpoint.json` — keyed by the repo and the parent issue number,
  not by the run's `${TMPDIR:-/tmp}/cenci/issue-<number>-<token>-*` scratch-file token — so a
  crash between a create's POST and the checkpoint write still resolves correctly on the next
  attempt: the resumed run re-lists marker-bearing candidates for the same nonce and recovers
  the already-created issue instead of creating a duplicate. A missing or corrupt checkpoint
  is treated as fail-closed (never a silent re-create), and the checkpoint is cleared only on
  a run's confirmed success — an aborted run retains it deliberately, so the next attempt has
  something to resume from.

- On re-entry, the authority for deciding whether a step needs to redo its work is externally
  effective state — the text GitHub or git itself will act on (a commit message, a PR body,
  GitHub's own `closingIssuesReferences`) — never a persisted verdict from an earlier turn. A
  persisted verdict is evidence of what an earlier turn believed, not proof of what is
  externally effective now; context compaction, a subagent re-invocation, or a fresh session
  can lose the persisted record entirely, and even an intact record can't rule out a manual
  edit or a partially-completed prior run. Concrete instance: `/cenci:implement` phase 9's
  Parent Close Gate reconciliation (`skills/implement/phases/phase-9-pr.md`, #879, 4/12 of
  #661), which re-reads the live commit message, PR body, and `closingIssuesReferences` on
  every entry and reconciles them to the current audit verdict, rather than trusting a
  previously-persisted verdict to decide whether reconciliation is even needed.

- When designing a fail-closed guard (a check that blocks operations on policy violations), distinguish explicitly between three cases: (1) the guarded resource doesn't exist yet (safe to proceed, e.g. a PR that hasn't been created on first entry), (2) the resource exists in an ambiguous or bad state (fail closed, stop and surface the issue), (3) we can't determine the resource's state due to infrastructure error (fail closed, surface the error). Conflating cases 1 and 2 causes the guard to hard-stop legitimate initial entries. Example: a guard checking if a PR exists with `gh pr view` must treat "no PR found" (first entry) differently from "PR is in bad state" — catching the not-yet-exists case and allowing safe continuation, while failing when the state is truly bad. A guard that collapses both into a single "fail closed" branch blocks every first-time run that legitimately lacks the guarded resource. (#879)

- Every hard stop after a `Working` claim in a multi-section escalating flow should funnel
  through one named, restated-at-each-call-site restoration routine rather than each section
  inventing its own ad hoc board-recovery prose — and a finalized artifact that could be
  malformed must never overwrite the last-known-good durable state in place; assemble it to a
  separate candidate and atomically replace only after validation. Concrete instance:
  `/cenci:implement`'s `## Restore Awaiting-Input State` routine (`skills/implement/phases/phase-1-plan.md`,
  #880, 3/12 of #661) — the single procedure `## Resume From Draft`, `## Unattended Escalation
  Path`, and `## Repair Escalation Anchor` all funnel through on every enumerated hard stop
  after `Working`, restoring `status: awaiting-input` plus the `Input Needed` label while
  preserving human answers and the original freshness baseline (`planCommitSha`/
  `stalenessPaths`) — paired with `## Resume From Draft` step 6's candidate-then-atomic-replace
  rule: the finalized plan assembles to `.plans/.<id>-<slug>.candidate.md` (dot-prefixed, so it
  never collides with `CheckPlan`'s `.plans/<id>-*.md` glob), is validated in place, and only
  then `mv`'d over the draft — a malformed candidate is left on disk for inspection while the
  last valid `awaiting-input` draft stays untouched.

- When creating or maintaining a large procedural-doc inventory or table that enumerates
  hard-stops, exceptions, or recovery scenarios (e.g. Hard-Stop Inventory in
  `skills/implement/phases/phase-1-plan.md`), verify before committing that: (1) any summary
  statement claiming to enumerate all entries (e.g. "exactly one non-restoring exception")
  accurately reflects all actual entries in the inventory — adding a new entry that changes
  this count must prompt update of the summary statement; (2) citations to recovery mechanisms
  explicitly document their full applicability conditions (e.g. "requires existing
  status: awaiting-input plan on disk") rather than oversimplifying conditional recovery into
  a single 'universal' backstop — when multiple recovery paths branch on different conditions,
  the doc must name which mechanism applies to which case. Summary drift and mischaracterized
  recovery citations cause reviewers to question coverage that is actually present but
  miscounted, and leave operators looking at the wrong tool when failures occur. Concrete
  instance: `/cenci:implement`'s `## Hard-Stop Inventory` table (`#880, 3/12 of #661`): the
  intro sentence must accurately count non-restoring exceptions, and Step 4 of `## Resume From
  Draft` must explicitly document that its backstop (`RecoveryResumeInterrupted`) requires an
  existing `status: awaiting-input` draft on disk and that step 1's "no valid draft exists at
  all" case has a different backstop (reconciler's stage-aware retry per #828).

- A skill's confirmation gate — not its preceding Q&A/back-and-forth loop — is the true first
  mutation boundary: no write of any kind (ownership claim, status label, or any other mutation)
  may occur before that gate's single confirmation, and every write step downstream must consume
  the gate's already-computed verdict rather than recomputing or re-asking. Concrete instance:
  `/cenci:refine`'s `## Confirmation Gate` (`skills/refine/SKILL.md`, #878, 3/12 of #661) — its
  Q&A relay loop only shapes the proposal's content, and `## Update Ticket` explicitly restates
  that no GitHub write occurs before that gate's Confirm branch.
