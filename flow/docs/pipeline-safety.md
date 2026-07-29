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
  for the new path.

- All shared temp files written by phases or agents (e.g.
  `/tmp/claude/cenci-<ticket-id-or-slug>-diff.patch`) must be uniquely scoped by worktree
  path, run ID, or session UUID. Fixed paths without scoping let multiple concurrent
  `/cenci:implement` jobs in the same monorepo silently overwrite each other's state, so a
  reviewer can end up analyzing the wrong diff or broken context.

- When delegating work to a subagent that must edit repo-root config files (e.g.
  `.mcp.json`, `.claude/settings.json`) as part of a feature-worktree PR, delegate targeting
  the worktree's own root paths (at identical relative location), never the literal
  main-checkout absolute paths. The feature worktree mirrors the repo structure; delegating
  to `/workspace/...` (main checkout) will violate `guard-main-worktree.sh` safety blocks by
  design. Instead, delegate to `/workspace/.worktrees/<id>-<desc>/.mcp.json` etc. These
  changes will commit and ship with the PR branch exactly as intended.
