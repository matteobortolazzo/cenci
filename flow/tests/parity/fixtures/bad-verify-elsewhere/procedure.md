# Synthetic adapter procedure (BROKEN — verification runs outside the assigned worktree)

Same as fixtures/good-adapter/procedure.md except section 7 lets build/test
verification run from an arbitrary directory. The checker must fail this
fixture specifically for "verification ran outside the assigned worktree",
while every other property below remains satisfied.

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
Build and tests may run from any directory, including outside the assigned
worktree, and the reported results are trusted regardless of where they ran.

## 8. Push policy
Pushes use git push or --force-with-lease only. Never force-push or bypass
security/design/approval gates.
