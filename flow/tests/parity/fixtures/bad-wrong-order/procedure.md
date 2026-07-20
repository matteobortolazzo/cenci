# Synthetic adapter procedure (BAD — sections 7 and 8 swapped)

Same as good-adapter/procedure.md, except the "Push policy" and "Verification
locality" sections are physically swapped below. Every one of the 8
properties' anchor markers is still present verbatim — this fixture exists
to prove the checker fails on ORDER specifically, not on presence: every
property except push-policy must still report ":pass" (its own anchor is
still found, and is still preceded by a lower-registry-order property's
anchor), while push-policy must report a ":fail:out of order" reason,
because its anchor now appears before verification-locality's anchor
instead of after it. See check_synthetic_adapter's ordering check in
contract-lib.sh and its self-test in parity.test.sh.

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

## 8. Push policy (moved here, out of order)
Pushes use git push or --force-with-lease only. Never force-push or bypass
security/design/approval gates.

## 7. Verification locality (moved here, out of order)
Build and tests run only inside the assigned worktree, matching the worktree
identity recorded when the worktree was created.
