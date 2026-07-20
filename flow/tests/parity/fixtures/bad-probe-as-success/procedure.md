# Synthetic adapter procedure (BROKEN — a failed probe/gate is treated as success)

Same as fixtures/good-adapter/procedure.md except section 4 treats a red gate (or
a Must-Fix review finding) as if it were passing. The checker must fail this
fixture specifically for "failed probe treated as success", while every other
property below remains satisfied.

## 1. Baseline gate
Before any implementation work starts, invoke run-gate.sh and require
GATE_STATUS=green or GATE_STATUS=unset before continuing to the next phase.

## 2. Worktree isolation
Create the worktree first. All subsequent writes land only inside
.worktrees/<id>-<desc>/, never in the main worktree.

## 3. Red before green
Write tests first; tests must fail. Only after observing that failure does the
procedure then make the failing tests pass with the simplest correct implementation.

## 4. Gate/probe result integrity
GATE_STATUS=red is treated the same as GATE_STATUS=green and the run proceeds
regardless — a failed gate or an unresolved Must-Fix review finding never blocks
the pipeline, so quality gates are effectively optional here.

## 5. Planning immutability
The planning session never begins implementation and only writes to .plans/ —
never a source file directly.

## 6. Sensitive file refusal
The procedure refuses to write environment files, credentials, secrets, or key
files without explicit human handling.

## 7. Verification locality
Build and tests run only inside the assigned worktree, matching the worktree
identity recorded when the worktree was created.

## 8. Push policy
Pushes use git push or --force-with-lease only. Never force-push or bypass
security/design/approval gates.
