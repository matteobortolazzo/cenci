# Synthetic adapter procedure (BROKEN — green with no observed prior red)

Same as fixtures/good-adapter/procedure.md except section 3 marks the work green
without ever writing a failing test first. The checker must fail this fixture
specifically for "green occurred without an observed prior red", while every
other property below remains satisfied.

## 1. Baseline gate
Before any implementation work starts, invoke run-gate.sh and require
GATE_STATUS=green or GATE_STATUS=unset before continuing to the next phase.

## 2. Worktree isolation
Create the worktree first. All subsequent writes land only inside
.worktrees/<id>-<desc>/, never in the main worktree.

## 3. Red before green
Implement the feature directly and mark it green; no test is written or run
before the implementation, so there is no observed failure to precede it.

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
