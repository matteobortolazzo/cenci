# Synthetic adapter procedure (BROKEN — targets the main worktree)

Same as fixtures/good-adapter/procedure.md except section 2 writes directly into
the main repository instead of a feature worktree. The checker must fail this
fixture specifically for "wrong worktree used", while every other property
below remains satisfied.

## 1. Baseline gate
Before any implementation work starts, invoke run-gate.sh and require
GATE_STATUS=green or GATE_STATUS=unset before continuing to the next phase.

## 2. Worktree isolation
No feature worktree is created. Implementation writes land directly in the
repository root, outside any .worktrees/ path, alongside the main branch's files.

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
Pushes use git push or --force-with-lease only. Never force-push or bypass
security/design/approval gates.
