# Dispatch Reconciler: Bounded-Retry Counter Lifecycle

Captures the lesson from ticket #265: when a bounded-retry counter is added alongside the pure-engine/impure-runner split in `internal/dispatch`, the counter's lifecycle must be independently derived from and cross-checked against ALL of the pure engine's possible outputs.

## Rules

- **Distinguish state-changing vs. informational side effects**: When applying a recovery, separate label mutations (state-changing on GitHub) from comments (informational only). Count failures and bump the retry counter only on state-change failures, never on comment-only failures. GitHub's label state may have correctly transitioned even if the trailing comment failed.

- **Clear the retry counter only for healthy tickets, not deferred verdicts**: A ticket with no recovery produced this pass is healthy ONLY if it also has no observation carried into `result.NextObservations` (dropped, not deferred). If the pure engine deferred a verdict (nil Snapshot, or durable attempt-count read failed), it carries the observation forward; the counter must survive too, or a ticket whose apply keeps failing during an outage will reset mid-incident and escape the budget. Test: check that `recoveredKeys` and `result.NextObservations` membership are both consulted before clearing.

- **Check all recovery kinds in integration tests, not just the happy path**: Initial tests may only cover `RecoveryRetry` (which does not double-append to result aggregates), missing bugs in `RecoveryFailed`/`RecoveryPlanInvalid` (which are unconditionally appended by the pure engine before any apply is attempted). When the impure runner then escalates a failed apply for these kinds, ensure the same ticket is not appended twice to result aggregates — track which keys are already present and make the escalation append idempotent.

- **Carry the grace observation forward on apply failure**: When an apply mutation fails, the pure engine assumed the observation would clear because the mutation would land. It didn't, so re-inject the original first-seen time from the loaded state rather than letting the grace clock silently restart. This preserves the timing across retries (same pattern as the pure engine's grace-period logic).

## Example Pattern

In `applyReconcile`:
1. Load persisted state with both `Observations` (grace clock) and `ApplyFailures` (retry counter).
2. Call pure `Reconcile()` engine → collects all tickets/plans/snapshot/attempts with prior state.
3. For each recovery:
   - Call `applyRecovery()` to apply state-changing mutations; track `mutated` (true if label edit succeeded, regardless of comment).
   - If `mutated`, delete counter and observation (recovery landed).
   - If NOT `mutated`, re-inject the prior observation and increment counter; if counter exceeds budget, escalate.
4. After all recoveries, clear stale counters: only clear if the key is absent from BOTH `recoveredKeys` (no recovery this pass) AND `result.NextObservations` (not deferred).
