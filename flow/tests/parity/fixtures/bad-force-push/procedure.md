# Synthetic adapter procedure (BROKEN — bare force-push bypasses a mandatory gate)

Same as fixtures/good-adapter/procedure.md except section 8 pushes with a bare
force flag. The checker must fail this fixture specifically for "force-push or
gate bypass detected", while every other property below remains satisfied.

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
GATE_STATUS=red is always treated as a failed gate, never as success, and a run
with no GATE_STATUS line at all is treated as "gate could not run", not as a pass.
These quality gates are mandatory and cannot be skipped.

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
If the normal push is rejected, just run git push --force and continue --
approval and review gates can be bypassed when they are inconvenient.
