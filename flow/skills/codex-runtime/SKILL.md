---
name: codex-runtime
description: Shared native Codex stage, checkpoint, goal, and agent-adapter contract.
user-invocable: false
---

# Native Codex workflow runtime

- Interactive planning workflows must begin in `/plan`. Gather every material choice
  there and produce an approved plan without mutating GitHub or project files.
- Continue mutations in a second normal-mode invocation of the same skill, passing the
  approved plan or `.plans/<file>` explicitly.
- Refinement is an exception to that two-invocation procedure: an ordinary normal-mode
  refine invocation asks its material questions, validates the proposal and applies it
  in the same invocation. `refinement.confirmBeforeApply: true` adds final review in
  that invocation; false or absent does not ask again. An explicit `/plan` refinement
  remains read-only and returns the proposal for a later normal-mode invocation.
  `skills/refine/codex.md` owns the pre-write checks and independent automerge policy.
- Persist phase state at `.cenci/checkpoints/<workflow>-<target>.json` with `node "${PLUGIN_ROOT}/codex/checkpoint.mjs" <init|advance|block|complete|clear> <path> <workflow> <target> <phase>`. The helper validates schema version 1 and uses atomic rename.
  Store workflow, target, phase, plan path, worktree, last completed gate, and status.
- On execution ambiguity, persist the checkpoint, clear the active goal, ask exactly one
  blocking question, and stop. Resume from the checkpoint after the answer.
- Arm a native Codex goal only after an approved implementation plan enters execution:
  call `create_goal({objective: "Complete approved plan <path> through an open PR"})` after
  verifying `get_goal({})` has no active goal. Clear it before human input/errors and after
  PR creation with `update_goal({status:"complete"})` only when the objective is achieved;
  otherwise checkpoint and return control so the client clears/pauses the active goal.
- Prefer generated `.codex/agents/*.toml` roles. If unavailable, delegate to a built-in
  worker with the same bounded role prompt and report that fallback in the checkpoint.
- Guard canary preflight: Codex skips pending or untrusted plugin hooks, so an installed
  flow plugin does not prove the write guards are watching the session. Every mutating Codex
  workflow (`implement` apply, `address-review`, `refactor`, and `babysit`'s Codex arm) runs
  `bash "${PLUGIN_ROOT}/codex/guard-canary.sh" plan` at the start of its normal-mode
  (mutating) invocation, after `project-core`'s configured-repo check and before its first
  mutation; `/plan` stays mutation-free and never runs it. The helper prints one `PROBE` line
  per (matcher group, guard) pair of `codex/hooks.json` (four today) plus `INSTRUCTION:` lines.
  Make exactly the listed tool calls, judge each from its own tool result (denied only when the
  guard's message shows, otherwise not-denied), then run the printed `check <run-dir> <n>=denied|not-denied…`
  command. Proceed only on `CANARY_STATUS=active`; on `CANARY_STATUS=refused`, or when `plan`
  cannot be made, a probe came back not-denied or inconclusive, or the helper cannot run, stop
  before any mutation, persist nothing, arm no goal, and report the helper's text verbatim: it
  names pending or untrusted hooks as the likely cause and points to `/hooks` to review them.
  The helper removes any canary that got written and reports it as `REMOVED: <path>`; a canary it
  cannot remove is reported as `LEFTOVER: <exact path>` for the user to delete by hand, and the
  workflow still refuses. In a sandbox session the output labels trust
  "hook trust bypassed by launcher (explicit flag)", never "trusted"; cenci only reports hook
  trust and never changes it. Claude Code `SKILL.md` files are excluded from this rule on purpose:
  Claude Code has no pending-trust state for plugin hooks, so there is nothing for a canary to
  detect there; that is the exclusion reason `docs/skill-authoring.md`'s client-surface rule asks
  to be recorded next to the declaration.
- Keep authenticated mutations, pushes, PR creation, and user questions in the root agent.
