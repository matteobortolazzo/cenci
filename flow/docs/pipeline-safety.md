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
  by durable, independently-verifiable state on disk (a marker file, checked with deterministic
  logic), not solely by in-context model recall. Context compaction or subagent re-invocation
  between turns can silently lose in-memory state; a written marker file survives the gap and
  enables fail-closed behavior (treat inconclusive checks as "gate active" rather than "gate
  absent").

- When delegating work to a subagent that must edit repo-root config files (e.g.
  `.mcp.json`, `.claude/settings.json`) as part of a feature-worktree PR, delegate targeting
  the worktree's own root paths (at identical relative location), never the literal
  main-checkout absolute paths. The feature worktree mirrors the repo structure; delegating
  to `/workspace/...` (main checkout) will violate `guard-main-worktree.sh` safety blocks by
  design. Instead, delegate to `/workspace/.worktrees/<id>-<desc>/.mcp.json` etc. These
  changes will commit and ship with the PR branch exactly as intended.
