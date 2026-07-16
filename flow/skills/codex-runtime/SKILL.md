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
- Persist phase state in `.cenci/checkpoints/<workflow>-<id>.json` using atomic rename.
  Store workflow, target, phase, plan path, worktree, last completed gate, and status.
- On execution ambiguity, persist the checkpoint, clear the active goal, ask exactly one
  blocking question, and stop. Resume from the checkpoint after the answer.
- Arm a native Codex goal only after an approved implementation plan enters execution.
  Clear it before human input, on unrecoverable errors, and immediately after PR creation.
- Prefer generated `.codex/agents/*.toml` roles. If unavailable, delegate to a built-in
  worker with the same bounded role prompt and report that fallback in the checkpoint.
- Keep authenticated mutations, pushes, PR creation, and user questions in the root agent.
