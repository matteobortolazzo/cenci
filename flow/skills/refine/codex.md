# Codex refine procedure

Read `project-core` and `codex-runtime`. Require `/plan`. Gather ticket context, classify
frontend/design scope, ask material scope and acceptance questions, and produce the refined
ticket proposal. Do not edit GitHub in Plan mode. Hand off `$cenci:refine apply <ticket>
<approved-plan>` to normal mode, then update the ticket and labels and clear the checkpoint.

Divergence: the refiner agent split is Claude-only — Codex has no subagent model tiering, so
this native procedure performs the refinement analysis inline as described above.
